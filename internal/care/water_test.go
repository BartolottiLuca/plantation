package care

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestCalibrationScenarios(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.July, Day: 1}
	watered := time.Date(2026, time.July, 1, 8, 0, 0, 0, time.UTC)
	events := []domain.CareEvent{{Kind: domain.Water, DoneAt: watered}}

	tests := []struct {
		name         string
		p            Params
		env          EnvSeries
		wantC        float64
		wantETc      float64
		wantInterval float64
	}{
		{
			name: "Monstera, 18 cm, indoor summer",
			p: Params{
				Location: domain.Indoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
				MinIntervalDays: 1, MaxIntervalDays: 90, BaseIntervalDays: 9,
				FExposure: 1, FRain: 0, PotDiameterMM: 180,
			},
			env:          EnvSeries{IndoorDataStale: true},
			wantC:        25.9,
			wantETc:      1.40,
			wantInterval: 9.3,
		},
		{
			name: "Outdoor shrub, 20 cm balcony",
			p: Params{
				Location: domain.Outdoor, Kc: 0.8, Substrate: domain.Peat, MAD: 0.5,
				MinIntervalDays: 1, MaxIntervalDays: 90, BaseIntervalDays: 4,
				FExposure: 1.3, FRain: 0.9, PotDiameterMM: 200,
			},
			env:          constantOutdoor(today, 4.0),
			wantC:        28.8,
			wantETc:      4.16,
			wantInterval: 3.5,
		},
		{
			name: "Tomato, 25 cm full sun, heat",
			p: Params{
				Location: domain.Outdoor, Kc: 1.15, Substrate: domain.Peat, MAD: 0.5,
				MinIntervalDays: 1, MaxIntervalDays: 90, BaseIntervalDays: 3,
				FExposure: 1.3, FRain: 0.9, PotDiameterMM: 250,
			},
			env:          constantOutdoor(today, 5.0),
			wantC:        36.0,
			wantETc:      7.48,
			wantInterval: 2.4,
		},
		{
			name: "Echeveria, 12 cm indoor",
			p: Params{
				Location: domain.Indoor, Kc: 0.25, Substrate: domain.Cactus, MAD: 0.8,
				MinIntervalDays: 1, MaxIntervalDays: 90, BaseIntervalDays: 14,
				FExposure: 1, FRain: 0, PotDiameterMM: 120,
			},
			env:          EnvSeries{IndoorDataStale: true},
			wantC:        8.6,
			wantETc:      0.50,
			wantInterval: 13.8,
		},
		{
			name: "Fern, 16 cm indoor",
			p: Params{
				Location: domain.Indoor, Kc: 1.0, Substrate: domain.Coir, MAD: 0.3,
				MinIntervalDays: 1, MaxIntervalDays: 90, BaseIntervalDays: 4,
				FExposure: 1, FRain: 0, PotDiameterMM: 160,
			},
			env:          EnvSeries{IndoorDataStale: true},
			wantC:        26.9,
			wantETc:      2.00,
			wantInterval: 4.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, expl := ScheduleWater(tt.p, events, tt.env, today)
			if relErr(expl.CapacityMM, tt.wantC) > 0.02 {
				t.Fatalf("capacity %.3f, want %.1f", expl.CapacityMM, tt.wantC)
			}
			if expl.MeanETcMMPerDay == 0 {
				t.Fatal("mean ETc is 0")
			}
			if relErr(expl.MeanETcMMPerDay, tt.wantETc) > 0.02 {
				t.Fatalf("ETc %.3f, want %.2f", expl.MeanETcMMPerDay, tt.wantETc)
			}
			got := expl.ThresholdMM / expl.MeanETcMMPerDay
			if relErr(got, tt.wantInterval) > 0.10 {
				t.Fatalf("interval %.3f, want %.1f (±10%%)", got, tt.wantInterval)
			}
		})
	}
}

func TestVoidedWaterEventIsIgnored(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 10}
	voidedAt := time.Date(2026, time.June, 9, 12, 0, 0, 0, time.UTC)
	old := time.Date(2026, time.June, 1, 8, 0, 0, 0, time.UTC)
	voided := time.Date(2026, time.June, 8, 8, 0, 0, 0, time.UTC)
	p := Params{
		Location: domain.Outdoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 9, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	events := []domain.CareEvent{
		{Kind: domain.Water, DoneAt: old},
		{Kind: domain.Water, DoneAt: voided, VoidedAt: &voidedAt},
	}
	dueVoided, _ := ScheduleWater(p, events, constantOutdoor(today, 3), today)
	dueOldOnly, _ := ScheduleWater(p, events[:1], constantOutdoor(today, 3), today)
	if dueVoided.On != dueOldOnly.On {
		t.Fatalf("voided event shifted due from %s to %s", dueOldOnly.On, dueVoided.On)
	}
}

func TestEmptyEnvFallsBackToBaseInterval(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Outdoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 9, MinIntervalDays: 4, MaxIntervalDays: 21,
		FExposure: 1, PotDiameterMM: 180,
	}
	due, expl := ScheduleWater(p, nil, EnvSeries{}, today)
	if expl.Mode != ModeBaseInterval {
		t.Fatalf("mode %q, want %q", expl.Mode, ModeBaseInterval)
	}
	if due.On != today.AddDays(9) {
		t.Fatalf("due %s, want %s", due.On, today.AddDays(9))
	}
}

func TestStaleIndoorUsesUnitFDry(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 9, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	_, expl := ScheduleWater(p, nil, EnvSeries{IndoorDataStale: true}, today)
	if expl.Mode != ModeWaterBalance {
		t.Fatalf("mode %q, want water_balance", expl.Mode)
	}
	if !expl.IndoorDataStale {
		t.Fatal("IndoorDataStale should be true")
	}
	if relErr(expl.MeanETcMMPerDay, 1.40) > 0.02 {
		t.Fatalf("ETc %.3f, want 1.40 from f_dry=1", expl.MeanETcMMPerDay)
	}
}

func TestOutOfRangeIndoorTreatedAsMissing(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 1, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 9, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	env := EnvSeries{
		Indoor: []IndoorDay{{
			Date: today, TempC: 80, HumidityPct: 5,
		}},
	}
	_, expl := ScheduleWater(p, nil, env, today)
	if expl.Mode != ModeWaterBalance {
		t.Fatalf("mode %q", expl.Mode)
	}
	// Faulty sample must not enter Tetens; missing day uses f_dry=1 → ET0=2.
	if relErr(expl.MeanETcMMPerDay, 2.0) > 0.05 {
		t.Fatalf("ETc %.3f; out-of-range sample was not treated as missing", expl.MeanETcMMPerDay)
	}
}

func TestIndoorNeverSeesOutdoorET0(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 1, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 9, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	env := EnvSeries{
		IndoorDataStale: true,
		Days:            constantOutdoor(today, 9.0).Days,
	}
	_, expl := ScheduleWater(p, nil, env, today)
	if relErr(expl.MeanETcMMPerDay, 2.0) > 0.05 {
		t.Fatalf("indoor ETc %.3f used outdoor ET0", expl.MeanETcMMPerDay)
	}
}

func TestDeferral(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 10}
	yesterday := today.AddDays(-1)
	p := Params{
		Location: domain.Outdoor, Kc: 1, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 3, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, FRain: 0.9, PotDiameterMM: 400,
	}
	// C = 0.30*320*0.6 = 57.6, threshold = 28.8. One 30 mm ET0 day after
	// watering yesterday crosses the threshold today.
	events := []domain.CareEvent{{
		Kind:   domain.Water,
		DoneAt: time.Date(yesterday.Year, yesterday.Month, yesterday.Day, 8, 0, 0, 0, time.UTC),
	}}

	withRain := func(p1, pr1, p2, pr2 float64) EnvSeries {
		days := []DayEnv{
			{Date: yesterday, ET0MM: 30, Observed: true},
			{Date: today, ET0MM: 30, Observed: true},
			{Date: today.AddDays(1), ET0MM: 1, PrecipMM: p1, PrecipProb: pr1},
			{Date: today.AddDays(2), ET0MM: 1, PrecipMM: p2, PrecipProb: pr2},
		}
		return EnvSeries{Days: days}
	}

	t.Run("all three conditions", func(t *testing.T) {
		due, expl := ScheduleWater(p, events, withRain(40, 80, 0, 0), today)
		if !expl.Deferred {
			t.Fatalf("want deferred, got %+v summary %q", due, expl.Summary)
		}
		if due.On != today.AddDays(1) {
			t.Fatalf("deferred to %s, want tomorrow", due.On)
		}
	})
	t.Run("rain too little", func(t *testing.T) {
		_, expl := ScheduleWater(p, events, withRain(1, 80, 0, 0), today)
		if expl.Deferred {
			t.Fatal("deferred when rain cannot cover deficit")
		}
	})
	t.Run("probability too low", func(t *testing.T) {
		_, expl := ScheduleWater(p, events, withRain(40, 20, 40, 20), today)
		if expl.Deferred {
			t.Fatal("deferred on a 20% forecast")
		}
	})
	t.Run("waiting reaches stress", func(t *testing.T) {
		hot := withRain(40, 80, 0, 0)
		for i := range hot.Days {
			if hot.Days[i].Date == today.AddDays(1) || hot.Days[i].Date == today.AddDays(2) {
				hot.Days[i].ET0MM = 80
			}
		}
		_, expl := ScheduleWater(p, events, hot, today)
		if expl.Deferred {
			t.Fatal("deferred into the stress zone")
		}
	})
	t.Run("mad too low", func(t *testing.T) {
		low := p
		low.MAD = 0.3
		_, expl := ScheduleWater(low, events, withRain(40, 80, 0, 0), today)
		if expl.Deferred {
			t.Fatal("deferred for MAD ≤ 0.35")
		}
	})
	t.Run("no rain factor", func(t *testing.T) {
		dry := p
		dry.FRain = 0
		_, expl := ScheduleWater(dry, events, withRain(40, 80, 0, 0), today)
		if expl.Deferred {
			t.Fatal("deferred when f_rain = 0")
		}
	})
	t.Run("never more than two days", func(t *testing.T) {
		due, expl := ScheduleWater(p, events, withRain(20, 80, 20, 80), today)
		if !expl.Deferred {
			t.Fatal("want a deferral")
		}
		if due.On.Sub(today) > 2 {
			t.Fatalf("deferred %d days", due.On.Sub(today))
		}
	})
	t.Run("not re-evaluated when already overdue", func(t *testing.T) {
		old := today.AddDays(-5)
		ev := []domain.CareEvent{{
			Kind:   domain.Water,
			DoneAt: time.Date(old.Year, old.Month, old.Day, 8, 0, 0, 0, time.UTC),
		}}
		due, expl := ScheduleWater(p, ev, withRain(40, 80, 0, 0), today)
		if expl.Deferred {
			t.Fatal("chained deferral on an overdue plant")
		}
		if due.Status != StatusOverdue {
			t.Fatalf("status %s, want overdue", due.Status)
		}
	})
}

func TestIntervalClamps(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Outdoor, Kc: 1.15, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 3, MinIntervalDays: 5, MaxIntervalDays: 6,
		FExposure: 1.3, PotDiameterMM: 250,
	}
	_, expl := ScheduleWater(p, nil, constantOutdoor(today, 5.0), today)
	if expl.ClampedBy != ClampMinInterval {
		t.Fatalf("ClampedBy %q, want min_interval (physics ~2.4d)", expl.ClampedBy)
	}
}

func TestProjectionCap(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.January, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 0.01, Substrate: domain.Cactus, MAD: 0.8,
		BaseIntervalDays: 30, MinIntervalDays: 1, MaxIntervalDays: 365,
		FExposure: 1, PotDiameterMM: 80, DormantMonths: []time.Month{time.January},
		DormancyFactor: 0.2,
	}
	due, expl := ScheduleWater(p, nil, EnvSeries{IndoorDataStale: true}, today)
	if expl.ClampedBy != ClampProjectionCap && due.On.Sub(today) < 60 {
		// either we hit the cap or min/max; a thirsty clamp would be a bug
		if expl.ClampedBy == ClampMinInterval {
			t.Fatalf("unexpected min clamp for a near-dormant cactus: %+v", expl)
		}
	}
}

func TestPropertyIntervalWithinBounds(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 15}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 400; i++ {
		minI := rng.IntN(10) + 1
		maxI := minI + rng.IntN(40)
		p := Params{
			Location:         pickLocation(rng),
			Kc:               weirdFloat(rng),
			Substrate:        pickSubstrate(rng),
			MAD:              weirdFloat(rng),
			BaseIntervalDays: rng.IntN(40) - 5,
			MinIntervalDays:  minI,
			MaxIntervalDays:  maxI,
			FExposure:        weirdFloat(rng),
			FRain:            weirdFloat(rng),
			PotDiameterMM:    rng.IntN(2000) - 10,
		}
		if rng.IntN(5) == 0 {
			p.PotDiameterMM = 0
		}
		env := EnvSeries{}
		switch rng.IntN(4) {
		case 0:
			// empty
		case 1:
			env.IndoorDataStale = true
		case 2:
			env = constantOutdoor(today, weirdFloat(rng))
			if rng.IntN(3) == 0 {
				env.Days[0].ET0MM = math.NaN()
			}
			if rng.IntN(3) == 0 && len(env.Days) > 2 {
				env.Days[2].PrecipMM = 1000
			}
		default:
			env.Indoor = []IndoorDay{{
				Date: today, TempC: weirdFloat(rng) * 80, HumidityPct: weirdFloat(rng) * 100,
			}}
		}
		var events []domain.CareEvent
		if rng.IntN(3) == 0 {
			events = []domain.CareEvent{{
				Kind:   domain.Water,
				DoneAt: time.Date(2026, time.June, 1+rng.IntN(14), 12, 0, 0, 0, time.UTC),
			}}
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("panic on iter %d: %v p=%+v", i, rec, p)
				}
			}()
			due, _ := ScheduleWater(p, events, env, today)
			start := startDate(p.AcquiredAt, events, domain.Water, today)
			n := due.On.Sub(start)
			wantMin, wantMax := normalizeBounds(minI, maxI)
			if n < wantMin || n > wantMax {
				t.Fatalf("iter %d interval %d outside [%d,%d] due=%s start=%s", i, n, wantMin, wantMax, due.On, start)
			}
		}()
	}
}

func constantOutdoor(today domain.Date, et0 float64) EnvSeries {
	var days []DayEnv
	for i := -meanET0Window; i <= projectionDays; i++ {
		d := today.AddDays(i)
		days = append(days, DayEnv{
			Date:     d,
			ET0MM:    et0,
			Observed: i < 0,
		})
	}
	return EnvSeries{Days: days}
}

func relErr(got, want float64) float64 {
	if want == 0 {
		return math.Abs(got)
	}
	return math.Abs(got-want) / math.Abs(want)
}

func pickLocation(rng *rand.Rand) domain.Location {
	if rng.IntN(2) == 0 {
		return domain.Indoor
	}
	return domain.Outdoor
}

func pickSubstrate(rng *rand.Rand) domain.SubstrateKind {
	switch rng.IntN(3) {
	case 0:
		return domain.Cactus
	case 1:
		return domain.Coir
	default:
		return domain.Peat
	}
}

func weirdFloat(rng *rand.Rand) float64 {
	switch rng.IntN(10) {
	case 0:
		return math.NaN()
	case 1:
		return math.Inf(1)
	case 2:
		return math.Inf(-1)
	case 3:
		return -rng.Float64() * 10
	default:
		return rng.Float64() * 5
	}
}
