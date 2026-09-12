package care

import (
	"math"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

const (
	fRoot          = 0.6
	potDepthFactor = 0.8
	thetaPeat      = 0.30
	thetaCactus    = 0.15
	thetaCoir      = 0.35
	vpdRefKPa      = 1.20
	et0IndoorRef   = 2.0
	fDryMin        = 0.5
	fDryMax        = 2.0
	projectionDays = 60
	meanET0Window  = 14
	rainLookahead  = 2
	rainCoverFrac  = 0.7
	rainMinProb    = 60.0
	rainStressFrac = 0.9
	maxDeferDays   = 2
	madNoDefer     = 0.35
)

func thetaAW(s domain.SubstrateKind) float64 {
	switch s {
	case domain.Cactus:
		return thetaCactus
	case domain.Coir:
		return thetaCoir
	default:
		return thetaPeat
	}
}

func capacityMM(p Params) float64 {
	if p.PotDiameterMM <= 0 || !finite(float64(p.PotDiameterMM)) {
		return 0
	}
	dPot := potDepthFactor * float64(p.PotDiameterMM)
	return thetaAW(p.Substrate) * dPot * fRoot
}

func dormancyFactor(p Params, month time.Month) float64 {
	for _, m := range p.DormantMonths {
		if m == month {
			if !finite(p.DormancyFactor) {
				return 1
			}
			return p.DormancyFactor
		}
	}
	return 1
}

func etcMM(p Params, et0 float64, month time.Month) float64 {
	if !finite(et0) || et0 < 0 {
		et0 = 0
	}
	kc := p.Kc
	if !finite(kc) || kc < 0 {
		kc = 0
	}
	exp := p.FExposure
	if !finite(exp) || exp < 0 {
		exp = 1
	}
	return kc * exp * dormancyFactor(p, month) * et0
}

func indoorET0(tempC, rh float64) (float64, bool) {
	if !finite(tempC) || !finite(rh) {
		return 0, false
	}
	if tempC < 0 || tempC > 45 || rh < 10 || rh > 95 {
		return 0, false
	}
	es := 0.6108 * math.Exp(17.27*tempC/(tempC+237.3))
	vpd := es * (1 - rh/100)
	if !finite(vpd) || vpd < 0 {
		return 0, false
	}
	fDry := clamp(math.Pow(vpd/vpdRefKPa, 0.7), fDryMin, fDryMax)
	if !finite(fDry) {
		return 0, false
	}
	return et0IndoorRef * fDry, true
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampDeficit(d, cap float64) float64 {
	if !finite(d) || d < 0 {
		return 0
	}
	if !finite(cap) || cap <= 0 {
		return 0
	}
	if d > cap {
		return cap
	}
	return d
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func normalizeBounds(min, max int) (int, int) {
	if min < 1 {
		min = 1
	}
	if max < min {
		max = min
	}
	return min, max
}

func civilDate(t time.Time) domain.Date {
	y, m, d := t.Date()
	return domain.Date{Year: y, Month: m, Day: d}
}

func lastEvent(events []domain.CareEvent, kind domain.TaskKind) (domain.CareEvent, bool) {
	var best domain.CareEvent
	found := false
	for _, e := range events {
		if e.Kind != kind || e.VoidedAt != nil {
			continue
		}
		if !found || e.DoneAt.After(best.DoneAt) {
			best = e
			found = true
		}
	}
	return best, found
}

func startDate(acquiredAt *time.Time, events []domain.CareEvent, kind domain.TaskKind, today domain.Date) domain.Date {
	if e, ok := lastEvent(events, kind); ok {
		return civilDate(e.DoneAt)
	}
	if acquiredAt != nil && !acquiredAt.IsZero() {
		return civilDate(*acquiredAt)
	}
	return today
}

func statusOf(on, today domain.Date) Status {
	if on.Before(today) {
		return StatusOverdue
	}
	if today.Before(on) {
		return StatusUpcoming
	}
	return StatusDueToday
}

func weekday(d domain.Date) string {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	return t.Weekday().String()[:3]
}
