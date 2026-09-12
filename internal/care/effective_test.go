package care

import (
	"testing"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestEffectiveFallsBackToSpecies(t *testing.T) {
	p := domain.Plant{
		Location:      domain.Indoor,
		FExposure:     1.0,
		FRain:         0.0,
		PotDiameterMM: 180,
	}
	s := domain.Species{
		Kc:               0.7,
		Substrate:        domain.Peat,
		MAD:              0.5,
		BaseIntervalDays: 9,
		MinIntervalDays:  4,
		MaxIntervalDays:  21,
		DormancyFactor:   1.0,
	}

	got := Effective(p, s)
	if got.Kc != 0.7 || got.Substrate != domain.Peat || got.MAD != 0.5 {
		t.Fatalf("species values not used: %+v", got)
	}
	if got.BaseIntervalDays != 9 || got.MinIntervalDays != 4 || got.MaxIntervalDays != 21 {
		t.Fatalf("interval values not used: %+v", got)
	}
	if got.Location != domain.Indoor || got.PotDiameterMM != 180 {
		t.Fatalf("plant placement not used: %+v", got)
	}
}

func TestEffectiveOverrideWins(t *testing.T) {
	kc := 1.1
	mad := 0.3
	sub := domain.Coir
	base, minI, maxI := 5, 2, 10
	p := domain.Plant{
		Location:      domain.Outdoor,
		FExposure:     1.3,
		FRain:         0.9,
		PotDiameterMM: 200,
		Overrides: domain.Overrides{
			Kc:               &kc,
			MAD:              &mad,
			Substrate:        &sub,
			BaseIntervalDays: &base,
			MinIntervalDays:  &minI,
			MaxIntervalDays:  &maxI,
		},
	}
	s := domain.Species{
		Kc:               0.7,
		Substrate:        domain.Peat,
		MAD:              0.5,
		BaseIntervalDays: 9,
		MinIntervalDays:  4,
		MaxIntervalDays:  21,
	}

	got := Effective(p, s)
	if got.Kc != 1.1 || got.MAD != 0.3 || got.Substrate != domain.Coir {
		t.Fatalf("overrides not applied: %+v", got)
	}
	if got.BaseIntervalDays != 5 || got.MinIntervalDays != 2 || got.MaxIntervalDays != 10 {
		t.Fatalf("interval overrides not applied: %+v", got)
	}
	if got.FExposure != 1.3 || got.FRain != 0.9 {
		t.Fatalf("plant factors lost: %+v", got)
	}
}
