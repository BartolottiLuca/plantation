package catalog

import (
	"fmt"
	"regexp"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// Value ranges from SPEC.md §7.2 (Kc, MAD, dormancy_factor) and the enums
// fixed by the species table's CHECK constraints (SPEC.md §5).
const (
	kcMin = 0.1
	kcMax = 1.5

	madMin = 0.2
	madMax = 0.9

	dormancyFactorMin = 0.2
	dormancyFactorMax = 1.0

	monthMin = 1
	monthMax = 12

	taskIntervalMin = 1
	taskIntervalMax = 3650
)

var validSubstrates = []string{"peat", "cactus", "coir"}
var validPlacements = []string{"indoor", "outdoor"}

// taskSlugPattern matches the same kebab-case a species slug is documented to
// use: lowercase letters, digits and single hyphens, never leading, trailing
// or doubled.
var taskSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validate checks one decoded species against every rule in SPEC.md §7.2 and
// the catalog CHECK constraints, returning every violation it finds rather
// than stopping at the first — an agent editing one file wants the whole list
// in one CI failure, not five round trips.
func validate(file, slug string, y speciesYAML) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s: %s", file, slug, fmt.Sprintf(format, args...)))
	}

	if y.Slug != "" && y.Slug != slug {
		fail("slug %q does not match filename slug %q", y.Slug, slug)
	}
	if y.CommonName == "" {
		fail("common_name is required")
	}
	if y.ScientificName == "" {
		fail("scientific_name is required")
	}

	placementOK := oneOf(y.Placement, validPlacements)
	if !placementOK {
		fail("placement %q not one of %v", y.Placement, validPlacements)
	}

	substrateOK := oneOf(y.Substrate, validSubstrates)
	if !substrateOK {
		fail("substrate %q not one of %v", y.Substrate, validSubstrates)
	}

	kcOK := inRange(y.Kc, kcMin, kcMax)
	if !kcOK {
		fail("kc %v out of range [%v, %v]", y.Kc, kcMin, kcMax)
	}

	madOK := inRange(y.MAD, madMin, madMax)
	if !madOK {
		fail("mad %v out of range [%v, %v]", y.MAD, madMin, madMax)
	}

	if y.DormancyFactor != nil && !inRange(*y.DormancyFactor, dormancyFactorMin, dormancyFactorMax) {
		fail("dormancy_factor %v out of range [%v, %v]", *y.DormancyFactor, dormancyFactorMin, dormancyFactorMax)
	}

	for _, m := range y.DormantMonths {
		if m < monthMin || m > monthMax {
			fail("dormant_months entry %d out of range [%d, %d]", m, monthMin, monthMax)
		}
	}

	validateTasks(fail, y.Tasks)

	baseOK := true
	if y.MinIntervalDays >= y.BaseIntervalDays {
		baseOK = false
		fail("min_interval_days %d not less than base_interval_days %d", y.MinIntervalDays, y.BaseIntervalDays)
	}
	if y.BaseIntervalDays >= y.MaxIntervalDays {
		baseOK = false
		fail("base_interval_days %d not less than max_interval_days %d", y.BaseIntervalDays, y.MaxIntervalDays)
	}

	// The physics check needs valid Kc/MAD/substrate/base to mean anything;
	// skip it rather than pile a confusing second error on top of the first.
	if kcOK && madOK && substrateOK && baseOK {
		if err := checkPhysicsConsistency(y); err != nil {
			fail("base_interval_days %d %s", y.BaseIntervalDays, err)
		}
	}

	return errs
}

// validateTasks checks every task slug is well-formed, unique within the
// species, and not the reserved "water" slug; every kind is in the closed
// vocabulary; every interval and month is in range; and every label is
// non-empty, since a blank label would ship a blank line in the digest.
func validateTasks(fail func(format string, args ...any), tasks []speciesTaskYAML) {
	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		if t.Slug == "" {
			fail("task with kind %q: slug is required", t.Kind)
			continue
		}
		if !taskSlugPattern.MatchString(t.Slug) {
			fail("task %q: slug must be kebab-case (lowercase letters, digits, single hyphens)", t.Slug)
		}
		if t.Slug == domain.WaterSlug {
			fail("task %q: %q is reserved — watering is scheduled from the reservoir model, not a species task", t.Slug, domain.WaterSlug)
		}
		if seen[t.Slug] {
			fail("task %q: slug is not unique within this species", t.Slug)
		}
		seen[t.Slug] = true

		if !domain.TaskKind(t.Kind).Valid() {
			fail("task %q: kind %q is not in the care vocabulary", t.Slug, t.Kind)
		}
		if t.Label == "" {
			fail("task %q: label is required", t.Slug)
		}
		if t.IntervalDays < taskIntervalMin || t.IntervalDays > taskIntervalMax {
			fail("task %q: interval_days %d out of range [%d, %d]", t.Slug, t.IntervalDays, taskIntervalMin, taskIntervalMax)
		}
		for _, m := range t.ActiveMonths {
			if m < monthMin || m > monthMax {
				fail("task %q: active_months entry %d out of range [%d, %d]", t.Slug, m, monthMin, monthMax)
			}
		}
	}
}

func inRange(v, lo, hi float64) bool {
	return finite(v) && v >= lo && v <= hi
}

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
