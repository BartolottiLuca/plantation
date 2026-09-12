package care

import (
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestScheduleFixedMovesOutsideActiveMonths(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	last := time.Date(2026, time.June, 1, 10, 0, 0, 0, time.UTC)
	task := domain.FixedTask{
		IntervalDays: 90,
		ActiveMonths: []time.Month{time.March, time.April, time.May, time.June},
	}
	events := []domain.CareEvent{{Kind: domain.Fertilize, DoneAt: last}}
	due, expl := ScheduleFixed(task, domain.Fertilize, events, today)
	if expl.Mode != ModeFixed {
		t.Fatalf("mode %q", expl.Mode)
	}
	// 1 June + 90d = 30 August, outside Mar–Jun → 1 March 2027.
	want := domain.Date{Year: 2027, Month: time.March, Day: 1}
	if due.On != want {
		t.Fatalf("due %s, want %s", due.On, want)
	}
}

func TestScheduleFixedEmptyMonthsStayPut(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	last := time.Date(2026, time.June, 1, 10, 0, 0, 0, time.UTC)
	task := domain.FixedTask{IntervalDays: 30}
	events := []domain.CareEvent{{Kind: domain.Prune, DoneAt: last}}
	due, _ := ScheduleFixed(task, domain.Prune, events, today)
	want := today.AddDays(30)
	if due.On != want {
		t.Fatalf("due %s, want %s", due.On, want)
	}
}

func TestScheduleFixedNoEventUsesToday(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	due, _ := ScheduleFixed(domain.FixedTask{IntervalDays: 14}, domain.Repot, nil, today)
	if due.On != today.AddDays(14) {
		t.Fatalf("due %s, want %s", due.On, today.AddDays(14))
	}
	if due.Kind != domain.Repot {
		t.Fatalf("kind %s", due.Kind)
	}
}
