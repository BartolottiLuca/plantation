package care

import (
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func ScheduleWater(p Params, events []domain.CareEvent, env EnvSeries, today domain.Date) (Due, Explanation) {
	minI, maxI := normalizeBounds(p.MinIntervalDays, p.MaxIntervalDays)
	start := startDate(p.AcquiredAt, events, domain.Water, today)
	expl := Explanation{
		OutdoorStale:    env.OutdoorStale,
		IndoorDataStale: env.IndoorDataStale,
	}

	capMM := capacityMM(p)
	expl.CapacityMM = capMM
	mad := p.MAD
	if !finite(mad) || mad <= 0 {
		mad = 0.5
	}
	if mad > 1 {
		mad = 1
	}
	threshold := mad * capMM
	expl.ThresholdMM = threshold

	series, ok := buildSeries(p, env, start, today)
	if !ok || capMM <= 0 || !finite(capMM) {
		return baseIntervalDue(p, start, today, minI, maxI, expl)
	}

	dueOn, deficit, meanETc, rainMM, clampedBy, deferred, deferReason := project(p, series, start, today, capMM, threshold)
	expl.Mode = ModeWaterBalance
	expl.DeficitMM = deficit
	expl.MeanETcMMPerDay = meanETc
	expl.EffectiveRainMM = rainMM
	expl.ClampedBy = clampedBy
	expl.Deferred = deferred
	expl.DeferredReason = deferReason
	if capMM > 0 && finite(capMM) {
		expl.DepletionPct = deficit / capMM
	}

	interval := dueOn.Sub(start)
	switch {
	case interval < minI:
		dueOn = start.AddDays(minI)
		expl.ClampedBy = ClampMinInterval
		expl.Deferred = false
		expl.DeferredReason = ""
	case interval > maxI:
		dueOn = start.AddDays(maxI)
		expl.ClampedBy = ClampMaxInterval
		expl.Deferred = false
		expl.DeferredReason = ""
	}

	due := Due{Kind: domain.Water, On: dueOn, Status: statusOf(dueOn, today)}
	expl.Summary = waterSummary(expl, due, today)
	return due, expl
}

func baseIntervalDue(p Params, start, today domain.Date, minI, maxI int, expl Explanation) (Due, Explanation) {
	n := p.BaseIntervalDays
	if n < 1 || n > 3650 {
		n = minI
	}
	clamped := ""
	if n < minI {
		n = minI
		clamped = ClampMinInterval
	}
	if n > maxI {
		n = maxI
		clamped = ClampMaxInterval
	}
	on := start.AddDays(n)
	expl.Mode = ModeBaseInterval
	expl.ClampedBy = clamped
	due := Due{Kind: domain.Water, On: on, Status: statusOf(on, today)}
	expl.Summary = waterSummary(expl, due, today)
	return due, expl
}

type envDay struct {
	et0        float64
	precip     float64
	precipProb float64
	estimated  bool
	ok         bool
}

func buildSeries(p Params, env EnvSeries, start, today domain.Date) (map[string]envDay, bool) {
	out := make(map[string]envDay)
	switch p.Location {
	case domain.Indoor:
		return buildIndoorSeries(env, start, today, out)
	default:
		return buildOutdoorSeries(env, start, today, out)
	}
}

func buildIndoorSeries(env EnvSeries, start, today domain.Date, out map[string]envDay) (map[string]envDay, bool) {
	if env.IndoorDataStale && len(env.Indoor) == 0 && len(env.Days) == 0 {
		// Stale with no samples: constant f_dry = 1.0 is still a usable series.
		horizon := today.AddDays(projectionDays)
		for d := start; !horizon.Before(d); d = d.AddDays(1) {
			out[d.String()] = envDay{et0: et0IndoorRef, ok: true}
		}
		return out, true
	}
	byDate := make(map[string]IndoorDay, len(env.Indoor))
	valid := 0
	for _, in := range env.Indoor {
		byDate[in.Date.String()] = in
		if _, ok := indoorET0(in.TempC, in.HumidityPct); ok {
			valid++
		}
	}
	if !env.IndoorDataStale && valid == 0 && len(env.Indoor) == 0 {
		return out, false
	}
	horizon := today.AddDays(projectionDays)
	for d := start; !horizon.Before(d); d = d.AddDays(1) {
		if env.IndoorDataStale {
			out[d.String()] = envDay{et0: et0IndoorRef, ok: true}
			continue
		}
		in, have := byDate[d.String()]
		if !have {
			out[d.String()] = envDay{et0: et0IndoorRef, ok: true}
			continue
		}
		et0, ok := indoorET0(in.TempC, in.HumidityPct)
		if !ok {
			out[d.String()] = envDay{et0: et0IndoorRef, ok: true}
			continue
		}
		out[d.String()] = envDay{et0: et0, ok: true}
	}
	return out, true
}

func buildOutdoorSeries(env EnvSeries, start, today domain.Date, out map[string]envDay) (map[string]envDay, bool) {
	if len(env.Days) == 0 {
		return out, false
	}
	mean, haveMean := trailingObservedMean(env.Days, today)
	for _, day := range env.Days {
		et0 := day.ET0MM
		if !finite(et0) || et0 < 0 {
			if haveMean {
				et0 = mean
			} else {
				continue
			}
		}
		precip := day.PrecipMM
		if !finite(precip) || precip < 0 {
			precip = 0
		}
		prob := day.PrecipProb
		if !finite(prob) || prob < 0 {
			prob = 0
		}
		out[day.Date.String()] = envDay{
			et0:        et0,
			precip:     precip,
			precipProb: prob,
			estimated:  day.Estimated,
			ok:         true,
		}
	}
	if len(out) == 0 && !haveMean {
		return out, false
	}
	horizon := today.AddDays(projectionDays)
	for d := start; !horizon.Before(d); d = d.AddDays(1) {
		if _, ok := out[d.String()]; ok {
			continue
		}
		if !haveMean {
			continue
		}
		out[d.String()] = envDay{et0: mean, estimated: true, ok: true}
	}
	return out, len(out) > 0
}

func trailingObservedMean(days []DayEnv, today domain.Date) (float64, bool) {
	from := today.AddDays(-meanET0Window)
	var sum float64
	n := 0
	for _, d := range days {
		if !d.Observed || d.Date.Before(from) || !d.Date.Before(today) {
			continue
		}
		if !finite(d.ET0MM) || d.ET0MM < 0 {
			continue
		}
		sum += d.ET0MM
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

func project(p Params, series map[string]envDay, start, today domain.Date, capMM, threshold float64) (due domain.Date, deficit, meanETc, rainMM float64, clampedBy string, deferred bool, deferReason string) {
	horizon := today.AddDays(projectionDays)
	var d float64
	// d keeps integrating past today to find the crossing day, so it ends the
	// loop saturated at C. The reported deficit is the value as of today —
	// anything later is a projection the user cannot act on.
	var todayDeficit float64
	var etcSum float64
	etcN := 0

	// The current run of at-or-above-threshold days (SPEC §7.2). Rain can end a
	// run, so the run in progress on `today` — not the first crossing ever — is
	// what makes a plant due, and is what a deferral anchors to.
	inRun := false
	var runStart domain.Date
	var runStartDeficit float64
	todayInRun := false
	var todayRunStart domain.Date
	var todayRunDeficit float64
	futureCrossSet := false
	var futureCross domain.Date
	var dueOn domain.Date

	for day := start; !horizon.Before(day); day = day.AddDays(1) {
		if day.Before(start) {
			continue
		}
		// Depletion starts the morning after the start date (reservoir is full
		// at the end of the watering / acquired day).
		if day == start {
			continue
		}
		ev, ok := series[day.String()]
		et0 := 0.0
		precip := 0.0
		if ok && ev.ok {
			et0 = ev.et0
			precip = ev.precip
		}
		etc := etcMM(p, et0, day.Month)
		if !finite(etc) {
			etc = 0
		}
		fr := p.FRain
		if !finite(fr) || fr < 0 {
			fr = 0
		}
		rain := fr * precip
		if !finite(rain) || rain < 0 {
			rain = 0
		}
		d = clampDeficit(d+etc-rain, capMM)

		if d+1e-12 >= threshold {
			if !inRun {
				inRun = true
				runStart = day
				runStartDeficit = d
			}
		} else {
			inRun = false
		}

		if day == today {
			todayDeficit = d
			todayInRun = inRun
			todayRunStart = runStart
			todayRunDeficit = runStartDeficit
		}
		if !day.Before(today) {
			etcSum += etc
			etcN++
			if !futureCrossSet && d+1e-12 >= threshold {
				futureCrossSet = true
				futureCross = day
			}
		}
	}

	if etcN > 0 {
		meanETc = etcSum / float64(etcN)
	}
	deficit = todayDeficit

	switch {
	case todayInRun:
		dueOn = todayRunStart
		def, reason, rEff := evaluateDeferral(p, series, todayRunStart, todayRunDeficit, capMM)
		// A deferral anchored to a crossing already past may have expired; the
		// plant is then plainly overdue and the window does not re-open.
		if def > 0 && !todayRunStart.AddDays(def).Before(today) {
			dueOn = todayRunStart.AddDays(def)
			deferred = true
			deferReason = reason
			rainMM = rEff
		}
	case futureCrossSet:
		dueOn = futureCross
	default:
		dueOn = horizon
		clampedBy = ClampProjectionCap
	}
	return dueOn, deficit, meanETc, rainMM, clampedBy, deferred, deferReason
}

// evaluateDeferral reads the lookahead from `from`, the crossing day, so the
// decision stays pinned to the due event instead of sliding forward.
func evaluateDeferral(p Params, series map[string]envDay, from domain.Date, dNow, capMM float64) (days int, reason string, rEff float64) {
	if p.MAD <= madNoDefer {
		return 0, "", 0
	}
	fr := p.FRain
	if !finite(fr) || fr <= 0 {
		return 0, "", 0
	}
	var etcAhead float64
	var maxProb float64
	for i := 1; i <= rainLookahead; i++ {
		day := from.AddDays(i)
		ev := series[day.String()]
		pmm := ev.precip
		if !finite(pmm) || pmm < 0 {
			pmm = 0
		}
		prob := ev.precipProb
		if !finite(prob) || prob < 0 {
			prob = 0
		}
		if prob > maxProb {
			maxProb = prob
		}
		rEff += fr * pmm * (prob / 100)
		etcAhead += etcMM(p, ev.et0, day.Month)
	}
	if rEff < rainCoverFrac*dNow {
		return 0, "", rEff
	}
	if maxProb < rainMinProb {
		return 0, "", rEff
	}
	if dNow+etcAhead >= rainStressFrac*capMM {
		return 0, "", rEff
	}

	// Prefer a 1-day deferral when the first day alone satisfies the rain cover.
	day1 := series[from.AddDays(1).String()]
	p1 := day1.precip
	if !finite(p1) || p1 < 0 {
		p1 = 0
	}
	pr1 := day1.precipProb
	if !finite(pr1) || pr1 < 0 {
		pr1 = 0
	}
	r1 := fr * p1 * (pr1 / 100)
	etc1 := etcMM(p, day1.et0, from.AddDays(1).Month)
	if r1 >= rainCoverFrac*dNow && pr1 >= rainMinProb && dNow+etc1 < rainStressFrac*capMM {
		return 1, fmt.Sprintf("%.0f mm rain forecast %s at %.0f%%", r1, weekday(from.AddDays(1)), pr1), r1
	}
	return maxDeferDays, fmt.Sprintf("%.0f mm rain forecast over two days (max %.0f%%)", rEff, maxProb), rEff
}

func waterSummary(e Explanation, due Due, today domain.Date) string {
	if e.Mode == ModeBaseInterval {
		n := due.On.Sub(today)
		switch due.Status {
		case StatusDueToday:
			return fmt.Sprintf("every %d days — due today", intervalOr(due, today))
		case StatusOverdue:
			return fmt.Sprintf("every %d days — overdue", intervalOr(due, today))
		default:
			if n < 0 {
				n = 0
			}
			return fmt.Sprintf("every %d days — due in %d days", intervalOr(due, today), n)
		}
	}
	if e.Deferred && e.DeferredReason != "" {
		return e.DeferredReason + " — deferred to " + weekday(due.On)
	}
	if e.Deferred {
		return fmt.Sprintf("%.0f mm rain — deferred to %s", e.EffectiveRainMM, weekday(due.On))
	}
	if e.ClampedBy == ClampProjectionCap {
		return "due in more than 60 days"
	}
	switch due.Status {
	case StatusDueToday:
		return fmt.Sprintf("deficit %.1f of %.1f mm — due today", e.DeficitMM, e.ThresholdMM)
	case StatusOverdue:
		return fmt.Sprintf("deficit %.1f of %.1f mm — overdue", e.DeficitMM, e.ThresholdMM)
	default:
		n := due.On.Sub(today)
		if n < 0 {
			n = 0
		}
		return fmt.Sprintf("deficit %.1f of %.1f mm — due in %d days", e.DeficitMM, e.ThresholdMM, n)
	}
}

func intervalOr(due Due, today domain.Date) int {
	n := due.On.Sub(today)
	if n < 1 {
		return 1
	}
	return n
}
