package care

import "github.com/BartolottiLuca/plantation/internal/domain"

// TaskControl is a per-plant override of a catalog task: whether it runs at
// all, how often, and whether it is snoozed. A kind with no control row keeps
// the species default, so an absent row and an enabled row behave alike.
type TaskControl struct {
	Kind                 domain.TaskKind
	Enabled              bool
	IntervalDaysOverride *int
	SnoozedUntil         *domain.Date
}

// ScheduledTask is one task a plant owes, with the explanation behind it.
type ScheduledTask struct {
	Kind domain.TaskKind
	Due  Due
	Expl Explanation
}

// ScheduleAll is the single answer to "what does this plant owe today".
// The dashboard and the Discord digest both call it; that shared call is what
// makes the two incapable of disagreeing (README, SPEC §6).
func ScheduleAll(p Params, s domain.Species, events []domain.CareEvent, env EnvSeries, controls []TaskControl, today domain.Date) []ScheduledTask {
	var out []ScheduledTask
	if TaskActive(controls, domain.Water, today) {
		due, expl := ScheduleWater(p, events, env, today)
		out = append(out, ScheduledTask{Kind: domain.Water, Due: due, Expl: expl})
	}

	for _, f := range []struct {
		kind domain.TaskKind
		task *domain.FixedTask
	}{
		{domain.Prune, s.Prune},
		{domain.Fertilize, s.Fertilize},
		{domain.Repot, s.Repot},
	} {
		if f.task == nil {
			continue
		}
		// SPEC §7.1: an in-ground plant is never repotted.
		if f.kind == domain.Repot && p.InGround {
			continue
		}
		if !TaskActive(controls, f.kind, today) {
			continue
		}
		t := *f.task
		if c, ok := controlFor(controls, f.kind); ok && c.IntervalDaysOverride != nil && *c.IntervalDaysOverride > 0 {
			t.IntervalDays = *c.IntervalDaysOverride
		}
		due, expl := ScheduleFixed(t, f.kind, events, today)
		out = append(out, ScheduledTask{Kind: f.kind, Due: due, Expl: expl})
	}
	return out
}

// TaskActive reports whether a task runs today. `snoozed_until` is the day the
// task comes back, not the day it goes quiet: the task is suppressed while
// today is before that date and runs again from it.
func TaskActive(controls []TaskControl, kind domain.TaskKind, today domain.Date) bool {
	c, ok := controlFor(controls, kind)
	if !ok {
		return true
	}
	if !c.Enabled {
		return false
	}
	if c.SnoozedUntil != nil && today.Before(*c.SnoozedUntil) {
		return false
	}
	return true
}

func controlFor(controls []TaskControl, kind domain.TaskKind) (TaskControl, bool) {
	for _, c := range controls {
		if c.Kind == kind {
			return c, true
		}
	}
	return TaskControl{}, false
}
