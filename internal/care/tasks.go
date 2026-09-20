package care

import "github.com/BartolottiLuca/plantation/internal/domain"

// TaskControl is a per-plant override of a species task: whether it runs at
// all, how often, and whether it is snoozed. It is keyed by task slug, not
// kind — a species may hold two tasks of one kind (lavender's hard spring
// prune and its light after-flowering trim), and each needs its own control.
// A slug with no control row keeps the species default, so an absent row and
// an enabled row behave alike. Slug is domain.WaterSlug for water's own row.
type TaskControl struct {
	Slug                 string
	Enabled              bool
	IntervalDaysOverride *int
	SnoozedUntil         *domain.Date
}

// ScheduledTask is one task a plant owes, with the explanation behind it.
// Slug and Label are carried here so a caller can render and address the
// task — including logging it or changing its control — without re-deriving
// either from the species task list.
type ScheduledTask struct {
	Slug  string
	Kind  domain.TaskKind
	Label string
	Due   Due
	Expl  Explanation
}

// waterLabel is what ScheduleAll calls watering in a ScheduledTask; it is not
// a domain.SpeciesTask; the label lives here because water has no entry in
// Species.Tasks to carry one.
const waterLabel = "Water"

// ScheduleAll is the single answer to "what does this plant owe today".
// The dashboard and the Discord digest both call it; that shared call is what
// makes the two incapable of disagreeing (README, SPEC §6).
func ScheduleAll(p Params, s domain.Species, events []domain.CareEvent, env EnvSeries, controls []TaskControl, today domain.Date) []ScheduledTask {
	var out []ScheduledTask
	if TaskActive(controls, domain.WaterSlug, today) {
		due, expl := ScheduleWater(p, events, env, today)
		out = append(out, ScheduledTask{Slug: domain.WaterSlug, Kind: domain.Water, Label: waterLabel, Due: due, Expl: expl})
	}

	for _, task := range s.Tasks {
		// SPEC §7.1: an in-ground plant is never repotted.
		if task.Kind == domain.Repot && p.InGround {
			continue
		}
		if !TaskActive(controls, task.Slug, today) {
			continue
		}
		if c, ok := controlFor(controls, task.Slug); ok && c.IntervalDaysOverride != nil && *c.IntervalDaysOverride > 0 {
			task.IntervalDays = *c.IntervalDaysOverride
		}
		due, expl := ScheduleFixed(task, events, today)
		out = append(out, ScheduledTask{Slug: task.Slug, Kind: task.Kind, Label: task.Label, Due: due, Expl: expl})
	}
	return out
}

// TaskActive reports whether a task runs today. `snoozed_until` is the day the
// task comes back, not the day it goes quiet: the task is suppressed while
// today is before that date and runs again from it.
func TaskActive(controls []TaskControl, slug string, today domain.Date) bool {
	c, ok := controlFor(controls, slug)
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

func controlFor(controls []TaskControl, slug string) (TaskControl, bool) {
	for _, c := range controls {
		if c.Slug == slug {
			return c, true
		}
	}
	return TaskControl{}, false
}
