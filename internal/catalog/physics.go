package catalog

import (
	"fmt"
	"math"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

// The physics-consistency check re-derives the water-balance interval at a
// fixed reference point (SPEC.md §7.6) so every species is checked against
// the same yardstick regardless of its own min/max bounds: 18cm pot,
// ET0=3mm/day, sheltered (f_exposure=1), no rain, no dormancy. base_interval
// is meant to be a plausible fallback for the *same* model, not an
// independently-invented number.
const (
	physicsPotDiameterMM = 180
	physicsET0MMPerDay   = 3.0
	physicsToleranceFrac = 0.40

	// Mirrors the trailing-mean window and projection cap in SPEC.md §7.2/§7.6
	// (internal/care keeps its own copies private; a constant ET0 series only
	// needs to span both to exercise the real code path).
	physicsTrailingDays   = 14
	physicsProjectionDays = 60
)

// checkPhysicsConsistency returns nil when y.BaseIntervalDays is within 40%
// of the interval care.ScheduleWater produces for y's Kc/MAD/substrate at the
// reference point, or an error fragment (no file/slug prefix — the caller
// adds that) describing the mismatch otherwise.
func checkPhysicsConsistency(y speciesYAML) error {
	modeled := physicsIntervalDays(y.Kc, y.MAD, domain.SubstrateKind(y.Substrate))
	if modeled <= 0 {
		return nil
	}
	declared := float64(y.BaseIntervalDays)
	diff := math.Abs(declared-float64(modeled)) / float64(modeled)
	if diff <= physicsToleranceFrac {
		return nil
	}
	return fmt.Errorf(
		"is %.0f%% away from the %d-day interval the model produces at ET0=3mm/day in an 18cm pot (must be within %.0f%%)",
		diff*100, modeled, physicsToleranceFrac*100,
	)
}

// physicsIntervalDays runs the real care.ScheduleWater at the reference point
// and returns the resulting interval in days from an arbitrary "today".
func physicsIntervalDays(kc, mad float64, substrate domain.SubstrateKind) int {
	today := domain.Date{Year: 2024, Month: time.January, Day: 15}
	days := make([]care.DayEnv, 0, physicsTrailingDays+physicsProjectionDays+1)
	for i := -physicsTrailingDays; i <= physicsProjectionDays; i++ {
		d := today.AddDays(i)
		days = append(days, care.DayEnv{
			Date:     d,
			ET0MM:    physicsET0MMPerDay,
			Observed: i < 0,
		})
	}
	params := care.Params{
		Location:        domain.Outdoor,
		Kc:              kc,
		Substrate:       substrate,
		MAD:             mad,
		MinIntervalDays: 1,
		MaxIntervalDays: physicsTrailingDays + physicsProjectionDays,
		FExposure:       1,
		FRain:           0,
		PotDiameterMM:   physicsPotDiameterMM,
	}
	due, _ := care.ScheduleWater(params, nil, care.EnvSeries{Days: days}, today)
	return due.On.Sub(today)
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
