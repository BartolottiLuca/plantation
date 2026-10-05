package species

import (
	"errors"
	"fmt"
	"testing"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

func proposalFor(kc, mad float64, sub domain.SubstrateKind) Proposal {
	return Proposal{
		CommonName:     "Test plant",
		ScientificName: "Testus plantus",
		Kc:             kc,
		Substrate:      sub,
		MAD:            mad,
		DormancyFactor: 1,
		MinTempC:       10,
	}
}

// The design claim the drafter rests on: computing the interval fields from
// kc/mad/substrate yields a species Validate accepts, for every combination of
// constants inside the documented ranges. The one exception is a modelled base
// interval of 1 day, where min >= 1 and min < base cannot both hold; that must
// be reported as a min_interval_days error, never stored.
func TestToSpeciesValidAcrossParameterGrid(t *testing.T) {
	substrates := []domain.SubstrateKind{domain.Peat, domain.Cactus, domain.Coir}
	var checked, oneDay int
	for _, sub := range substrates {
		// Integer steps: accumulating 0.1 overshoots the range's own upper bound.
		for i := 1; i <= 15; i++ {
			kc := float64(i) / 10
			for j := 2; j <= 9; j++ {
				mad := float64(j) / 10
				name := fmt.Sprintf("kc=%.1f/mad=%.1f/%s", kc, mad, sub)
				s := ToSpecies(proposalFor(kc, mad, sub))
				errs := catalog.Validate(s)
				checked++

				if s.BaseIntervalDays == 1 {
					oneDay++
					t.Logf("1-day corner: %s", name)
					if len(errs) != 1 {
						t.Errorf("%s: base is 1 day, want exactly the min_interval_days error, got %v", name, errs)
						continue
					}
					var fe *catalog.FieldError
					if !errors.As(errs[0], &fe) || fe.Field != "min_interval_days" {
						t.Errorf("%s: base is 1 day, want a min_interval_days error, got %v", name, errs[0])
					}
					continue
				}
				if len(errs) != 0 {
					t.Errorf("%s: base %d rejected: %v", name, s.BaseIntervalDays, errs)
				}
				if s.MinIntervalDays < 1 || s.MinIntervalDays >= s.BaseIntervalDays || s.BaseIntervalDays >= s.MaxIntervalDays {
					t.Errorf("%s: want 1 <= min < base < max, got %d/%d/%d", name, s.MinIntervalDays, s.BaseIntervalDays, s.MaxIntervalDays)
				}
			}
		}
	}
	t.Logf("checked %d combinations; %d have a 1-day modelled interval", checked, oneDay)
}

// The factors are only defensible if they contain what a person curated. These
// are the eight species the app shipped with, as a person curated them: a generated range around the
// same base must be at least as wide on both sides.
func TestIntervalFactorsContainTheCuratedSpecies(t *testing.T) {
	curated := []struct {
		slug         string
		base, lo, hi int
	}{
		{"echeveria-elegans", 14, 7, 45},
		{"ficus-lyrata", 6, 3, 18},
		{"hydrangea-macrophylla", 4, 1, 10},
		{"lavandula-angustifolia", 8, 4, 21},
		{"monstera-deliciosa", 6, 3, 21},
		{"nephrolepis-exaltata", 3, 1, 10},
		{"ocimum-basilicum", 4, 1, 10},
		{"sansevieria-trifasciata", 10, 5, 30},
	}
	for _, c := range curated {
		// Reproduce ToSpecies's arithmetic for an arbitrary base, since kc/mad
		// that model exactly this base are not the point here.
		lo, hi := IntervalBounds(c.base)
		if lo > c.lo || hi < c.hi {
			t.Errorf("%s: generated %d..%d does not contain curated %d..%d around base %d", c.slug, lo, hi, c.lo, c.hi, c.base)
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Monstera deliciosa", "monstera-deliciosa"},
		{"  Ficus   lyrata  ", "ficus-lyrata"},
		{"Mentha × piperita 'Citrata'", "mentha-piperita-citrata"},
		{"Hydrangea macrophylla 'Annabelle'", "hydrangea-macrophylla-annabelle"},
		{"Sansevieria trifasciata", "sansevieria-trifasciata"},
		{"Zamioculcas zamiifolia", "zamioculcas-zamiifolia"},
		{"Ünïcode plantus", "n-code-plantus"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSlugifyProducesValidSlugs(t *testing.T) {
	for _, in := range []string{"Monstera deliciosa", "Mentha × piperita", "Ficus lyrata 'Bambino'"} {
		s := ToSpecies(Proposal{ScientificName: in, CommonName: "x",
			Kc: 0.7, MAD: 0.5, Substrate: domain.Peat, DormancyFactor: 1, MinTempC: 10})
		for _, e := range catalog.Validate(s) {
			var fe *catalog.FieldError
			if errors.As(e, &fe) && fe.Field == "slug" {
				t.Errorf("%q -> slug %q rejected: %v", in, s.Slug, e)
			}
		}
	}
}
