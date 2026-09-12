package care

import (
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func ScheduleFixed(t domain.FixedTask, kind domain.TaskKind, events []domain.CareEvent, today domain.Date) (Due, Explanation) {
	start := today
	if e, ok := lastEvent(events, kind); ok {
		start = civilDate(e.DoneAt)
	}
	n := t.IntervalDays
	if n < 1 {
		n = 1
	}
	on := bumpToActiveMonth(start.AddDays(n), t.ActiveMonths)
	due := Due{Kind: kind, On: on, Status: statusOf(on, today)}
	expl := Explanation{
		Mode:    ModeFixed,
		Summary: fixedSummary(kind, due, today),
	}
	return due, expl
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

func fixedSummary(kind domain.TaskKind, due Due, today domain.Date) string {
	name := string(kind)
	switch due.Status {
	case StatusDueToday:
		return fmt.Sprintf("%s — due today", name)
	case StatusOverdue:
		return fmt.Sprintf("%s — overdue", name)
	default:
		n := due.On.Sub(today)
		if n < 0 {
			n = 0
		}
		return fmt.Sprintf("%s — due in %d days", name, n)
	}
}
