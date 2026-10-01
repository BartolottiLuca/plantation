package catalog

import (
	"fmt"
	"regexp"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// Value ranges from SPEC.md §7.2 (Kc, MAD, dormancy_factor) and the enums
// fixed by the species table's CHECK constraints (SPEC.md §5). Exported so the
// schema a model is asked to fill in states the same bounds the validator
// enforces, from one place.
const (
	KcMin = 0.1
	KcMax = 1.5

	MADMin = 0.2
	MADMax = 0.9

	DormancyFactorMin = 0.2
	DormancyFactorMax = 1.0

	// A sanity bound, not a horticultural claim: it exists so a typo or a
	// hallucination cannot store 300 as a frost threshold.
	MinTempMin = -60
	MinTempMax = 40

	TaskIntervalMin = 1
	TaskIntervalMax = 3650
)

const (
	monthMin = 1
	monthMax = 12
)

var validSubstrates = []domain.SubstrateKind{domain.Peat, domain.Cactus, domain.Coir}
var validPlacements = []domain.Location{domain.Indoor, domain.Outdoor}

// slugPattern is kebab-case: lowercase letters, digits and single hyphens,
// never leading, trailing or doubled. It applies to species and task slugs.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// FieldError is one validation failure, tagged with the form field it belongs
// to so a caller can show it next to the input. Error returns the message
// alone; the message already names the field and the offending value.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

// SpeciesTaskKinds is the kinds a species may declare as a task. It is the
// care vocabulary minus the two that no species task drives: water, which is
// scheduled from the reservoir model, and inspect, which is log-only because
// there is no defensible interval for "have a look" (SPEC.md §3).
func SpeciesTaskKinds() []domain.TaskKind {
	var out []domain.TaskKind
	for _, k := range domain.TaskKinds() {
		if k == domain.Water || k == domain.Inspect {
			continue
		}
		out = append(out, k)
	}
	return out
}

// Validate checks a species against every rule in SPEC.md §7.2 and the species
// table's CHECK constraints, returning every violation it finds rather than
// stopping at the first — someone editing a form wants the whole list in one
// round trip, not five. Every returned error is a *FieldError.
func Validate(s domain.Species) []error {
	var errs []error
	fail := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if s.Slug == "" {
		fail("slug", "slug is required")
	} else if !slugPattern.MatchString(s.Slug) {
		fail("slug", "slug %q must be kebab-case (lowercase letters, digits, single hyphens)", s.Slug)
	}
	if s.CommonName == "" {
		fail("common_name", "common_name is required")
	}
	if s.ScientificName == "" {
		fail("scientific_name", "scientific_name is required")
	}

	substrateOK := containsValue(validSubstrates, s.Substrate)
	if !substrateOK {
		fail("substrate", "substrate %q not one of %v", s.Substrate, validSubstrates)
	}

	kcOK := inRange(s.Kc, KcMin, KcMax)
	if !kcOK {
		fail("kc", "kc %v out of range [%v, %v]", s.Kc, KcMin, KcMax)
	}

	madOK := inRange(s.MAD, MADMin, MADMax)
	if !madOK {
		fail("mad", "mad %v out of range [%v, %v]", s.MAD, MADMin, MADMax)
	}

	if !inRange(s.DormancyFactor, DormancyFactorMin, DormancyFactorMax) {
		fail("dormancy_factor", "dormancy_factor %v out of range [%v, %v]", s.DormancyFactor, DormancyFactorMin, DormancyFactorMax)
	}

	if !inRange(s.MinTempC, MinTempMin, MinTempMax) {
		fail("min_temp_c", "min_temp_c %v out of range [%v, %v]", s.MinTempC, float64(MinTempMin), float64(MinTempMax))
	}

	validateMonths(fail, "dormant_months", "dormant_months", s.DormantMonths)
	validateTasks(fail, s.Tasks)

	baseOK := true
	if s.MinIntervalDays >= s.BaseIntervalDays {
		baseOK = false
		fail("min_interval_days", "min_interval_days %d not less than base_interval_days %d", s.MinIntervalDays, s.BaseIntervalDays)
	}
	if s.MinIntervalDays < 1 {
		baseOK = false
		fail("min_interval_days", "min_interval_days %d must be at least 1", s.MinIntervalDays)
	}
	if s.BaseIntervalDays >= s.MaxIntervalDays {
		baseOK = false
		fail("max_interval_days", "base_interval_days %d not less than max_interval_days %d", s.BaseIntervalDays, s.MaxIntervalDays)
	}

	// The physics check needs valid Kc/MAD/substrate/base to mean anything;
	// skip it rather than pile a confusing second error on top of the first.
	if kcOK && madOK && substrateOK && baseOK {
		if err := checkPhysicsConsistency(s.Kc, s.MAD, s.Substrate, s.BaseIntervalDays); err != nil {
			fail("base_interval_days", "base_interval_days %d %s", s.BaseIntervalDays, err)
		}
	}

	return errs
}

// validateTasks checks every task slug is well-formed, unique within the
// species, and not the reserved "water" slug; every kind is one a species may
// declare; every interval and month is in range; and every label is non-empty,
// since a blank label would ship a blank line in the digest.
func validateTasks(fail func(field, format string, args ...any), tasks []domain.SpeciesTask) {
	seen := make(map[string]bool, len(tasks))
	for i, t := range tasks {
		field := func(name string) string { return fmt.Sprintf("tasks[%d].%s", i, name) }

		if t.Slug == "" {
			fail(field("slug"), "task with kind %q: slug is required", t.Kind)
			continue
		}
		if !slugPattern.MatchString(t.Slug) {
			fail(field("slug"), "task %q: slug must be kebab-case (lowercase letters, digits, single hyphens)", t.Slug)
		}
		if t.Slug == domain.WaterSlug {
			fail(field("slug"), "task %q: %q is reserved — watering is scheduled from the reservoir model, not a species task", t.Slug, domain.WaterSlug)
		}
		if seen[t.Slug] {
			fail(field("slug"), "task %q: slug is not unique within this species", t.Slug)
		}
		seen[t.Slug] = true

		switch {
		case !t.Kind.Valid():
			fail(field("kind"), "task %q: kind %q is not in the care vocabulary", t.Slug, t.Kind)
		case t.Kind == domain.Water:
			fail(field("kind"), "task %q: kind %q is scheduled from the reservoir model, not declared as a species task", t.Slug, t.Kind)
		case t.Kind == domain.Inspect:
			fail(field("kind"), "task %q: kind %q is log-only; no species task drives it", t.Slug, t.Kind)
		}
		if t.Label == "" {
			fail(field("label"), "task %q: label is required", t.Slug)
		}
		if t.OnlyIn != "" && !containsValue(validPlacements, t.OnlyIn) {
			fail(field("only_in"), "task %q: only_in %q not one of %v, or empty for anywhere", t.Slug, t.OnlyIn, validPlacements)
		}
		if t.IntervalDays < TaskIntervalMin || t.IntervalDays > TaskIntervalMax {
			fail(field("interval_days"), "task %q: interval_days %d out of range [%d, %d]", t.Slug, t.IntervalDays, TaskIntervalMin, TaskIntervalMax)
		}
		validateMonths(fail, field("active_months"), fmt.Sprintf("task %q: active_months", t.Slug), t.ActiveMonths)
	}
}

// validateMonths reports every out-of-range or repeated month. label is the
// prefix of the message, which need not equal the form field name.
func validateMonths(fail func(field, format string, args ...any), field, label string, months []time.Month) {
	seen := make(map[time.Month]bool, len(months))
	for _, m := range months {
		if m < monthMin || m > monthMax {
			fail(field, "%s entry %d out of range [%d, %d]", label, int(m), monthMin, monthMax)
			continue
		}
		if seen[m] {
			fail(field, "%s entry %d appears more than once", label, int(m))
		}
		seen[m] = true
	}
}

func inRange(v, lo, hi float64) bool {
	return finite(v) && v >= lo && v <= hi
}

func containsValue[T comparable](allowed []T, v T) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
