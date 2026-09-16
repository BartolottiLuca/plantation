package catalog

import (
	"fmt"
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
)

var validSubstrates = []string{"peat", "cactus", "coir"}
var validPlacements = []string{"indoor", "outdoor"}

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
	validateFixedTaskMonths(fail, "prune", y.Prune)
	validateFixedTaskMonths(fail, "fertilize", y.Fertilize)
	validateFixedTaskMonths(fail, "repot", y.Repot)

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

func validateFixedTaskMonths(fail func(format string, args ...any), field string, t *fixedTaskYAML) {
	if t == nil {
		return
	}
	if t.IntervalDays < 1 {
		fail("%s.interval_days %d out of range [%d, <unbounded>]", field, t.IntervalDays, 1)
	}
	for _, m := range t.ActiveMonths {
		if m < monthMin || m > monthMax {
			fail("%s.active_months entry %d out of range [%d, %d]", field, m, monthMin, monthMax)
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
