package care

import (
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestEffectiveFallsBackToSpecies(t *testing.T) {
	p := domain.Plant{
		Location:      domain.Indoor,
		FExposure:     1.0,
		FRain:         0.0,
		PotDiameterCM: 18,
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
		t.Fatalf("plant placement not used, or cm->mm conversion wrong: %+v", got)
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
		PotDiameterCM: 20,
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

func TestEffectiveCopiesInGround(t *testing.T) {
	p := domain.Plant{Location: domain.Outdoor, InGround: true, FExposure: 1.3, FRain: 0.9}
	got := Effective(p, domain.Species{Kc: 0.7, Substrate: domain.Peat, MAD: 0.5, BaseIntervalDays: 9, MinIntervalDays: 4, MaxIntervalDays: 21})
	if !got.InGround || got.Location != domain.Outdoor {
		t.Fatalf("in-ground not copied: %+v", got)
	}
}

// The species' rest period is an indoor fact (SPEC §7.2): an outdoor plant gets
// its winter slowdown from a low ET0, and applying dormancy on top would count it
// twice.
func TestEffectiveAppliesDormancyOnlyIndoors(t *testing.T) {
	s := domain.Species{
		Kc: 0.4, Substrate: domain.Cactus, MAD: 0.7,
		BaseIntervalDays: 8, MinIntervalDays: 2, MaxIntervalDays: 28,
		DormantMonths:  []time.Month{time.November, time.December, time.January, time.February},
		DormancyFactor: 0.5,
	}

	indoor := Effective(domain.Plant{Location: domain.Indoor, FExposure: 1, PotDiameterCM: 18}, s)
	if len(indoor.DormantMonths) != 4 || indoor.DormancyFactor != 0.5 {
		t.Errorf("indoor plant: dormancy = %v x%v, want the species' rest period", indoor.DormantMonths, indoor.DormancyFactor)
	}

	outdoor := Effective(domain.Plant{Location: domain.Outdoor, FExposure: 1, FRain: 0.9, PotDiameterCM: 18}, s)
	if len(outdoor.DormantMonths) != 0 || outdoor.DormancyFactor != 1 {
		t.Errorf("outdoor plant: dormancy = %v x%v, want none (it is already in the weather)", outdoor.DormantMonths, outdoor.DormancyFactor)
	}
}

// The consequence that matters: declaring a rest period changes an indoor
// plant's schedule in its rest months and leaves an outdoor plant's untouched.
func TestRestPeriodMovesIndoorScheduleOnly(t *testing.T) {
	resting := domain.Species{
		Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 6, MinIntervalDays: 1, MaxIntervalDays: 60,
		DormantMonths: []time.Month{time.January, time.February}, DormancyFactor: 0.5,
	}
	never := resting
	never.DormantMonths, never.DormancyFactor = nil, 1

	today := domain.Date{Year: 2026, Month: time.January, Day: 10}
	var days []DayEnv
	for i := -14; i <= 60; i++ {
		days = append(days, DayEnv{Date: today.AddDays(i), ET0MM: 2, Observed: i < 0})
	}
	envs := map[domain.Location]EnvSeries{
		// Indoors with no Tado data the model runs on its reference ET0.
		domain.Indoor:  {IndoorDataStale: true},
		domain.Outdoor: {Days: days},
	}
	interval := func(loc domain.Location, s domain.Species) int {
		p := Effective(domain.Plant{Location: loc, FExposure: 1, PotDiameterCM: 18}, s)
		due, _ := ScheduleWater(p, nil, envs[loc], today)
		return due.On.Sub(today)
	}

	if r, n := interval(domain.Indoor, resting), interval(domain.Indoor, never); r <= n {
		t.Errorf("indoor: resting %d days vs no rest %d days; the rest period should stretch the interval", r, n)
	}
	if r, n := interval(domain.Outdoor, resting), interval(domain.Outdoor, never); r != n {
		t.Errorf("outdoor: resting %d days vs no rest %d days; outdoors the rest period must not apply", r, n)
	}
}
