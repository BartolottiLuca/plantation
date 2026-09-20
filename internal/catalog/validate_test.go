package catalog

import (
	"os"
	"strings"
	"testing"
)

func TestValidFixtureHasNoErrors(t *testing.T) {
	species, err := loadFS(os.DirFS("testdata/valid"), "testdata/valid")
	if err != nil {
		t.Fatalf("valid fixture failed to load: %v", err)
	}
	if len(species) != 1 {
		t.Fatalf("got %d species, want 1", len(species))
	}
}

// Each case is a single-file fixture under testdata/ exercising exactly one
// validation failure mode. The assertion checks the message names the file,
// the slug and the field, per AGENTS.md / SPEC.md §7.6.
func TestValidationFailureModes(t *testing.T) {
	const slug = "test-species"

	tests := []struct {
		name    string
		dir     string
		wantAll []string // substrings that must all appear in the aggregate error
	}{
		{
			name: "unknown field",
			dir:  "unknown_field",
			wantAll: []string{
				"testdata/unknown_field/test-species.yaml",
				slug,
				"waters_itself",
			},
		},
		{
			name: "out-of-range kc",
			dir:  "bad_kc",
			wantAll: []string{
				"testdata/bad_kc/test-species.yaml",
				slug,
				"kc 2.4 out of range [0.1, 1.5]",
			},
		},
		{
			name: "missing required field",
			dir:  "missing_required",
			wantAll: []string{
				"testdata/missing_required/test-species.yaml",
				slug,
				"common_name is required",
			},
		},
		{
			name: "min_interval_days >= base_interval_days",
			dir:  "bad_interval_order",
			wantAll: []string{
				"testdata/bad_interval_order/test-species.yaml",
				slug,
				"min_interval_days 10 not less than base_interval_days 6",
			},
		},
		{
			name: "base_interval_days inconsistent with physics",
			dir:  "bad_physics",
			wantAll: []string{
				"testdata/bad_physics/test-species.yaml",
				slug,
				"base_interval_days 60",
				"away from the",
				"model produces at ET0=3mm/day in an 18cm pot",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirLabel := "testdata/" + tt.dir
			_, err := loadFS(os.DirFS(dirLabel), dirLabel)
			if err == nil {
				t.Fatalf("loadFS(%s) succeeded, want a validation error", dirLabel)
			}
			msg := err.Error()
			for _, want := range tt.wantAll {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not contain %q", msg, want)
				}
			}
		})
	}
}

func TestSlugMustMatchFilename(t *testing.T) {
	y := speciesYAML{
		Slug:             "not-the-filename",
		CommonName:       "X",
		ScientificName:   "Y",
		Placement:        "indoor",
		Kc:               0.7,
		Substrate:        "peat",
		MAD:              0.5,
		BaseIntervalDays: 6,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	if len(errs) == 0 {
		t.Fatal("want a slug-mismatch error")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "test-species") && strings.Contains(e.Error(), "not-the-filename") {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors %v do not name the slug mismatch", errs)
	}
}

func TestMonthsOutOfRangeRejected(t *testing.T) {
	y := speciesYAML{
		CommonName:       "X",
		ScientificName:   "Y",
		Placement:        "indoor",
		Kc:               0.7,
		Substrate:        "peat",
		MAD:              0.5,
		BaseIntervalDays: 6,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
		DormantMonths:    []int{0, 13},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	if len(errs) < 2 {
		t.Fatalf("want an error per out-of-range month, got %v", errs)
	}
}

func TestDormancyFactorOutOfRangeRejected(t *testing.T) {
	bad := 1.5
	y := speciesYAML{
		CommonName:       "X",
		ScientificName:   "Y",
		Placement:        "indoor",
		Kc:               0.7,
		Substrate:        "peat",
		MAD:              0.5,
		BaseIntervalDays: 6,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
		DormancyFactor:   &bad,
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "dormancy_factor 1.5 out of range [0.2, 1]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want dormancy_factor range error, got %v", errs)
	}
}

func TestPlacementAndSubstrateEnumsRejected(t *testing.T) {
	y := speciesYAML{
		CommonName:       "X",
		ScientificName:   "Y",
		Placement:        "greenhouse",
		Kc:               0.7,
		Substrate:        "loam",
		MAD:              0.5,
		BaseIntervalDays: 6,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	if !strings.Contains(joined, `placement "greenhouse" not one of`) {
		t.Errorf("want placement enum error, got %q", joined)
	}
	if !strings.Contains(joined, `substrate "loam" not one of`) {
		t.Errorf("want substrate enum error, got %q", joined)
	}
}

func baseValidYAML() speciesYAML {
	return speciesYAML{
		CommonName:       "X",
		ScientificName:   "Y",
		Placement:        "indoor",
		Kc:               0.7,
		Substrate:        "peat",
		MAD:              0.5,
		BaseIntervalDays: 6,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
	}
}

func TestTaskDuplicateSlugRejected(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "prune", Kind: "prune", Label: "Prune", IntervalDays: 90},
		{Slug: "prune", Kind: "prune", Label: "Prune again", IntervalDays: 30},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), `task "prune": slug is not unique`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a duplicate-slug error, got %v", errs)
	}
}

func TestTaskUnknownKindRejected(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "misting", Kind: "misting", Label: "Mist", IntervalDays: 7},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), `kind "misting" is not in the care vocabulary`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an unknown-kind error, got %v", errs)
	}
}

func TestTaskWaterSlugReserved(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "water", Kind: "water", Label: "Water", IntervalDays: 7},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), `"water" is reserved`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a reserved-slug error, got %v", errs)
	}
}

func TestTaskEmptyLabelRejected(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "prune", Kind: "prune", Label: "", IntervalDays: 90},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), `task "prune": label is required`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an empty-label error, got %v", errs)
	}
}

func TestTaskSlugNotKebabCaseRejected(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "Pinch_Flowers", Kind: "pinch", Label: "Pinch", IntervalDays: 14},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "must be kebab-case") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a kebab-case error, got %v", errs)
	}
}

func TestTaskIntervalDaysOutOfRangeRejected(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "prune", Kind: "prune", Label: "Prune", IntervalDays: 0},
		{Slug: "feed", Kind: "fertilize", Label: "Feed", IntervalDays: 4000},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	if !strings.Contains(joined, `task "prune": interval_days 0 out of range`) {
		t.Errorf("want a too-low interval error, got %q", joined)
	}
	if !strings.Contains(joined, `task "feed": interval_days 4000 out of range`) {
		t.Errorf("want a too-high interval error, got %q", joined)
	}
}

func TestTaskActiveMonthsOutOfRangeRejected(t *testing.T) {
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "prune", Kind: "prune", Label: "Prune", IntervalDays: 90, ActiveMonths: []int{0, 13}},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	if len(errs) < 2 {
		t.Fatalf("want an error per out-of-range month, got %v", errs)
	}
}

func TestTwoTasksOfOneKindAreValid(t *testing.T) {
	// The whole reason task identity exists: lavender's two prunings must not
	// be rejected as duplicates just because they share a kind.
	y := baseValidYAML()
	y.Tasks = []speciesTaskYAML{
		{Slug: "spring-tidy", Kind: "prune", Label: "Spring tidy", IntervalDays: 365, ActiveMonths: []int{3}},
		{Slug: "prune-after-flowering", Kind: "prune", Label: "Cut back after flowering", IntervalDays: 365, ActiveMonths: []int{8}},
	}
	errs := validate("catalog/species/test-species.yaml", "test-species", y)
	if len(errs) != 0 {
		t.Fatalf("two tasks sharing a kind should validate cleanly, got %v", errs)
	}
}
