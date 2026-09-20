package care

import (
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// ScheduleFixed applies §7.7: a fixed interval plus optional active months,
// with no physics. task carries the identity (Slug, Kind, Label) and the
// interval/months to apply — the caller (ScheduleAll) has already applied any
// per-plant interval override before calling this.
func ScheduleFixed(task domain.SpeciesTask, events []domain.CareEvent, today domain.Date) (Due, Explanation) {
	start := today
	if e, ok := lastTaskEvent(events, task); ok {
		start = civilDate(e.DoneAt)
	}
	n := task.IntervalDays
	if n < 1 {
		n = 1
	}
	on := bumpToActiveMonth(start.AddDays(n), task.ActiveMonths)
	due := Due{Kind: task.Kind, On: on, Status: statusOf(on, today)}
	expl := Explanation{
		Mode:    ModeFixed,
		Summary: fixedSummary(task.Label, due, today),
	}
	return due, expl
}

// lastTaskEvent finds the most recent non-voided event that satisfies task:
// one whose TaskSlug matches task.Slug, or — for an event logged before tasks
// had identity — one with no TaskSlug at all whose Kind matches task.Kind
// (SPEC §7.7). A single legacy event of a kind therefore satisfies every task
// of that kind until each has been logged once under its own slug; that decays
// on its own as real events accumulate, and guessing which task an old event
// meant would be worse.
func lastTaskEvent(events []domain.CareEvent, task domain.SpeciesTask) (domain.CareEvent, bool) {
	var best domain.CareEvent
	found := false
	for _, e := range events {
		if e.VoidedAt != nil {
			continue
		}
		var matches bool
		if e.TaskSlug != nil {
			matches = *e.TaskSlug == task.Slug
		} else {
			matches = e.Kind == task.Kind
		}
		if !matches {
			continue
		}
		if !found || e.DoneAt.After(best.DoneAt) {
			best = e
			found = true
		}
	}
	return best, found
}

func bumpToActiveMonth(d domain.Date, months []time.Month) domain.Date {
	if len(months) == 0 {
		return d
	}
	active := make(map[time.Month]bool, len(months))
	for _, m := range months {
		active[m] = true
	}
	if active[d.Month] {
		return d
	}
	year, month := d.Year, d.Month
	for range 12 {
		month++
		if month > 12 {
			month = 1
			year++
		}
		if active[month] {
			return domain.Date{Year: year, Month: month, Day: 1}
		}
	}
	return d
}

// fixedSummary names the task by its label, not its kind: two tasks can share
// a kind (lavender's two prunings), and "prune — due today" for both would be
// indistinguishable in the one place meant to explain which is which.
func fixedSummary(label string, due Due, today domain.Date) string {
	switch due.Status {
	case StatusDueToday:
		return fmt.Sprintf("%s — due today", label)
	case StatusOverdue:
		return fmt.Sprintf("%s — overdue", label)
	default:
		n := due.On.Sub(today)
		if n < 0 {
			n = 0
		}
		return fmt.Sprintf("%s — due in %d days", label, n)
	}
}
