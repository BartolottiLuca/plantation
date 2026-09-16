package catalog

import (
	"sort"
	"testing"
)

func TestLoadRealCatalogSucceeds(t *testing.T) {
	species, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := []string{
		"echeveria-elegans",
		"ficus-lyrata",
		"hydrangea-macrophylla",
		"lavandula-angustifolia",
		"monstera-deliciosa",
		"nephrolepis-exaltata",
		"ocimum-basilicum",
		"sansevieria-trifasciata",
	}
	if len(species) != len(want) {
		t.Fatalf("got %d species, want %d (template must be excluded)", len(species), len(want))
	}

	got := make([]string, len(species))
	for i, s := range species {
		got[i] = s.Slug
	}
	sort.Strings(got)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slug[%d] = %q, want %q (full list: %v)", i, got[i], want[i], got)
		}
	}
}

func TestLoadRealCatalogSpeciesPassPhysicsConsistency(t *testing.T) {
	species, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	for _, s := range species {
		if err := checkPhysicsConsistency(speciesYAML{
			Kc:               s.Kc,
			Substrate:        string(s.Substrate),
			MAD:              s.MAD,
			BaseIntervalDays: s.BaseIntervalDays,
		}); err != nil {
			t.Errorf("%s: base_interval_days %d %v", s.Slug, s.BaseIntervalDays, err)
		}
	}
}
