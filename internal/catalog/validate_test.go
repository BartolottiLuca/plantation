package catalog

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// validSpecies is a species that passes Validate. Its base interval is taken
// from the model rather than hard-coded, so these tests pin validator
// behaviour without depending on the water-balance numbers.
func validSpecies(t *testing.T) domain.Species {
	t.Helper()
	const kc, mad = 0.7, 0.5
	base := ModelledIntervalDays(kc, mad, domain.Peat)
	if base < 3 {
		t.Fatalf("reference interval %d too small for the fixture to bracket it", base)
	}
	return domain.Species{
		Slug:             "test-species",
		CommonName:       "Test species",
		ScientificName:   "Testus speciesus",
		Kc:               kc,
		Substrate:        domain.Peat,
		MAD:              mad,
		BaseIntervalDays: base,
		MinIntervalDays:  1,
		MaxIntervalDays:  base * 3,
		DormancyFactor:   1,
		MinTempC:         10,
	}
}

func messages(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error() + "\n")
	}
	return b.String()
}

func TestValidSpeciesHasNoErrors(t *testing.T) {
	if errs := Validate(validSpecies(t)); len(errs) != 0 {
		t.Fatalf("want no errors, got:\n%s", messages(errs))
	}
}

func TestValidateReportsEveryFailureWithItsField(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.Species)
		wantField string
		wantMsg   string
	}{
		{"slug missing", func(s *domain.Species) { s.Slug = "" }, "slug", "slug is required"},
		{"slug not kebab-case", func(s *domain.Species) { s.Slug = "Not_Kebab" }, "slug", "must be kebab-case"},
		{"common name missing", func(s *domain.Species) { s.CommonName = "" }, "common_name", "common_name is required"},
		{"scientific name missing", func(s *domain.Species) { s.ScientificName = "" }, "scientific_name", "scientific_name is required"},
		{"substrate unknown", func(s *domain.Species) { s.Substrate = "loam" }, "substrate", `substrate "loam" not one of`},
		{"kc above range", func(s *domain.Species) { s.Kc = 2.4 }, "kc", "kc 2.4 out of range [0.1, 1.5]"},
		{"kc below range", func(s *domain.Species) { s.Kc = 0.01 }, "kc", "out of range"},
		{"mad above range", func(s *domain.Species) { s.MAD = 0.95 }, "mad", "mad 0.95 out of range [0.2, 0.9]"},
		{"dormancy factor above range", func(s *domain.Species) { s.DormancyFactor = 1.5 }, "dormancy_factor", "dormancy_factor 1.5 out of range [0.2, 1]"},
		{"dormancy factor unset", func(s *domain.Species) { s.DormancyFactor = 0 }, "dormancy_factor", "out of range"},
		{"min temp absurd", func(s *domain.Species) { s.MinTempC = 300 }, "min_temp_c", "min_temp_c 300 out of range"},
		{"dormant month zero", func(s *domain.Species) { s.DormantMonths = []time.Month{0} }, "dormant_months", "dormant_months entry 0 out of range [1, 12]"},
		{"dormant month thirteen", func(s *domain.Species) { s.DormantMonths = []time.Month{13} }, "dormant_months", "dormant_months entry 13 out of range [1, 12]"},
		{"dormant month repeated", func(s *domain.Species) { s.DormantMonths = []time.Month{11, 12, 11} }, "dormant_months", "entry 11 appears more than once"},
		{"min interval not below base", func(s *domain.Species) { s.MinIntervalDays = s.BaseIntervalDays }, "min_interval_days", "not less than base_interval_days"},
		{"min interval zero", func(s *domain.Species) { s.MinIntervalDays = 0 }, "min_interval_days", "must be at least 1"},
		{"base interval not below max", func(s *domain.Species) { s.MaxIntervalDays = s.BaseIntervalDays }, "max_interval_days", "not less than max_interval_days"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpecies(t)
			tt.mutate(&s)
			errs := Validate(s)
			for _, e := range errs {
				var fe *FieldError
				if !errors.As(e, &fe) {
					t.Fatalf("error %v is not a *FieldError", e)
				}
				if fe.Field == tt.wantField && strings.Contains(fe.Message, tt.wantMsg) {
					return
				}
			}
			t.Fatalf("want field %q with message containing %q, got:\n%s", tt.wantField, tt.wantMsg, messages(errs))
		})
	}
}

func TestBaseIntervalInconsistentWithPhysicsIsRejected(t *testing.T) {
	s := validSpecies(t)
	s.BaseIntervalDays *= 4
	s.MaxIntervalDays = s.BaseIntervalDays * 3
	errs := Validate(s)
	got := messages(errs)
	for _, want := range []string{"base_interval_days", "away from the", "model produces at ET0=3mm/day in an 18cm pot"} {
		if !strings.Contains(got, want) {
			t.Errorf("errors %q do not contain %q", got, want)
		}
	}
}

func TestModelledBaseIntervalPassesPhysicsByConstruction(t *testing.T) {
	// The claim a drafted species relies on: computing base from kc/mad/substrate
	// cannot fail the consistency check, across the whole plausible parameter grid.
	for _, sub := range validSubstrates {
		for kc := KcMin; kc <= KcMax+1e-9; kc += 0.2 {
			for mad := MADMin; mad <= MADMax+1e-9; mad += 0.1 {
				base := ModelledIntervalDays(kc, mad, sub)
				if err := checkPhysicsConsistency(kc, mad, sub, base); err != nil {
					t.Errorf("kc=%.2f mad=%.2f %s: base %d rejected: %v", kc, mad, sub, base, err)
				}
			}
		}
	}
}

func TestFailuresAreAllReportedTogether(t *testing.T) {
	s := validSpecies(t)
	s.Kc = 9
	s.MAD = 9
	s.CommonName = ""
	if errs := Validate(s); len(errs) < 3 {
		t.Fatalf("want at least 3 errors reported together, got:\n%s", messages(errs))
	}
}

func TestTaskRules(t *testing.T) {
	tests := []struct {
		name      string
		tasks     []domain.SpeciesTask
		wantField string
		wantMsg   string
	}{
		{
			"duplicate slug",
			[]domain.SpeciesTask{
				{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 90},
				{Slug: "prune", Kind: domain.Prune, Label: "Prune again", IntervalDays: 30},
			},
			"tasks[1].slug", `task "prune": slug is not unique`,
		},
		{
			"unknown kind",
			[]domain.SpeciesTask{{Slug: "misting", Kind: "misting", Label: "Mist", IntervalDays: 7}},
			"tasks[0].kind", `kind "misting" is not in the care vocabulary`,
		},
		{
			"water slug is reserved",
			[]domain.SpeciesTask{{Slug: "water", Kind: domain.Prune, Label: "Water", IntervalDays: 7}},
			"tasks[0].slug", `"water" is reserved`,
		},
		{
			"water kind is not a species task",
			[]domain.SpeciesTask{{Slug: "soak", Kind: domain.Water, Label: "Soak", IntervalDays: 7}},
			"tasks[0].kind", "scheduled from the reservoir model",
		},
		{
			"inspect kind is log-only",
			[]domain.SpeciesTask{{Slug: "look", Kind: domain.Inspect, Label: "Look", IntervalDays: 7}},
			"tasks[0].kind", "log-only",
		},
		{
			"only_in unknown",
			[]domain.SpeciesTask{{Slug: "mulch", Kind: domain.Mulch, Label: "Mulch", IntervalDays: 365, OnlyIn: "greenhouse"}},
			"tasks[0].only_in", `only_in "greenhouse" not one of`,
		},
		{
			"empty label",
			[]domain.SpeciesTask{{Slug: "prune", Kind: domain.Prune, Label: "", IntervalDays: 90}},
			"tasks[0].label", `task "prune": label is required`,
		},
		{
			"slug not kebab-case",
			[]domain.SpeciesTask{{Slug: "Pinch_Flowers", Kind: domain.Pinch, Label: "Pinch", IntervalDays: 14}},
			"tasks[0].slug", "must be kebab-case",
		},
		{
			"slug missing",
			[]domain.SpeciesTask{{Kind: domain.Prune, Label: "Prune", IntervalDays: 90}},
			"tasks[0].slug", "slug is required",
		},
		{
			"interval too low",
			[]domain.SpeciesTask{{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 0}},
			"tasks[0].interval_days", `task "prune": interval_days 0 out of range`,
		},
		{
			"interval too high",
			[]domain.SpeciesTask{{Slug: "feed", Kind: domain.Fertilize, Label: "Feed", IntervalDays: 4000}},
			"tasks[0].interval_days", `task "feed": interval_days 4000 out of range`,
		},
		{
			"active month out of range",
			[]domain.SpeciesTask{{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 90, ActiveMonths: []time.Month{13}}},
			"tasks[0].active_months", "active_months entry 13 out of range",
		},
		{
			"active month repeated",
			[]domain.SpeciesTask{{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 90, ActiveMonths: []time.Month{3, 3}}},
			"tasks[0].active_months", "entry 3 appears more than once",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpecies(t)
			s.Tasks = tt.tasks
			for _, e := range Validate(s) {
				var fe *FieldError
				if errors.As(e, &fe) && fe.Field == tt.wantField && strings.Contains(fe.Message, tt.wantMsg) {
					return
				}
			}
			t.Fatalf("want field %q with message containing %q, got:\n%s", tt.wantField, tt.wantMsg, messages(Validate(s)))
		})
	}
}

func TestTwoTasksOfOneKindAreValid(t *testing.T) {
	// The whole reason task identity exists: lavender's two prunings must not
	// be rejected as duplicates just because they share a kind.
	s := validSpecies(t)
	s.Tasks = []domain.SpeciesTask{
		{Slug: "spring-tidy", Kind: domain.Prune, Label: "Spring tidy", IntervalDays: 365, ActiveMonths: []time.Month{3}},
		{Slug: "prune-after-flowering", Kind: domain.Prune, Label: "Cut back after flowering", IntervalDays: 365, ActiveMonths: []time.Month{8}},
	}
	if errs := Validate(s); len(errs) != 0 {
		t.Fatalf("two tasks sharing a kind should validate cleanly, got:\n%s", messages(errs))
	}
}

func TestTaskRestrictedToOnePlacementIsValid(t *testing.T) {
	for _, loc := range []domain.Location{domain.Indoor, domain.Outdoor} {
		s := validSpecies(t)
		s.Tasks = []domain.SpeciesTask{{Slug: "mulch-bed", Kind: domain.Mulch, Label: "Mulch the bed", IntervalDays: 365, OnlyIn: loc}}
		if errs := Validate(s); len(errs) != 0 {
			t.Errorf("only_in %q should validate, got:\n%s", loc, messages(errs))
		}
	}
}

func TestSpeciesTaskKindsExcludeWaterAndInspect(t *testing.T) {
	for _, k := range SpeciesTaskKinds() {
		if k == domain.Water || k == domain.Inspect {
			t.Errorf("SpeciesTaskKinds contains %q", k)
		}
		s := validSpecies(t)
		s.Tasks = []domain.SpeciesTask{{Slug: "task", Kind: k, Label: "Do it", IntervalDays: 30}}
		if errs := Validate(s); len(errs) != 0 {
			t.Errorf("kind %q is declarable but Validate rejects it:\n%s", k, messages(errs))
		}
	}
	if got, want := len(SpeciesTaskKinds()), len(domain.TaskKinds())-2; got != want {
		t.Errorf("SpeciesTaskKinds has %d kinds, want %d", got, want)
	}
}
