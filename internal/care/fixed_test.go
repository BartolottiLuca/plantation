package care

import (
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestScheduleFixedMovesOutsideActiveMonths(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	last := time.Date(2026, time.June, 1, 10, 0, 0, 0, time.UTC)
	task := domain.SpeciesTask{
		Slug: "feed", Kind: domain.Fertilize, Label: "Feed",
		IntervalDays: 90,
		ActiveMonths: []time.Month{time.March, time.April, time.May, time.June},
	}
	slug := "feed"
	events := []domain.CareEvent{{Kind: domain.Fertilize, TaskSlug: &slug, DoneAt: last}}
	due, expl := ScheduleFixed(task, events, today)
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
	task := domain.SpeciesTask{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 30}
	slug := "prune"
	events := []domain.CareEvent{{Kind: domain.Prune, TaskSlug: &slug, DoneAt: last}}
	due, _ := ScheduleFixed(task, events, today)
	want := today.AddDays(30)
	if due.On != want {
		t.Fatalf("due %s, want %s", due.On, want)
	}
}

func TestScheduleFixedNoEventUsesToday(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	task := domain.SpeciesTask{Slug: "repot", Kind: domain.Repot, Label: "Repot", IntervalDays: 14}
	due, _ := ScheduleFixed(task, nil, today)
	if due.On != today.AddDays(14) {
		t.Fatalf("due %s, want %s", due.On, today.AddDays(14))
	}
	if due.Kind != domain.Repot {
		t.Fatalf("kind %s", due.Kind)
	}
}

func TestScheduleFixedUsesTaskLabelNotKind(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	task := domain.SpeciesTask{Slug: "pinch-flowers", Kind: domain.Pinch, Label: "Pinch flower spikes", IntervalDays: 14}
	due, expl := ScheduleFixed(task, nil, today)
	want := "Pinch flower spikes — due in 14 days"
	if expl.Summary != want {
		t.Fatalf("summary %q, want %q", expl.Summary, want)
	}
	if due.Kind != domain.Pinch {
		t.Fatalf("kind %s, want pinch", due.Kind)
	}
}

func TestLastTaskEventSlugTakesPriorityOverKind(t *testing.T) {
	// Two tasks share a kind — lavender's hard spring prune and its light
	// after-flowering trim — and each must track its own last-done date
	// rather than collapsing onto whichever prune event is newest.
	hardSlug := "prune-hard"
	trimSlug := "trim-after-flowering"
	march := time.Date(2026, time.March, 15, 9, 0, 0, 0, time.UTC)
	august := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)
	events := []domain.CareEvent{
		{Kind: domain.Prune, TaskSlug: &hardSlug, DoneAt: march},
		{Kind: domain.Prune, TaskSlug: &trimSlug, DoneAt: august},
	}

	hard := domain.SpeciesTask{Slug: hardSlug, Kind: domain.Prune, Label: "Hard prune", IntervalDays: 365}
	got, ok := lastTaskEvent(events, hard)
	if !ok || !got.DoneAt.Equal(march) {
		t.Fatalf("prune-hard last event = %+v, ok=%v, want March", got, ok)
	}

	trim := domain.SpeciesTask{Slug: trimSlug, Kind: domain.Prune, Label: "Trim after flowering", IntervalDays: 365}
	got, ok = lastTaskEvent(events, trim)
	if !ok || !got.DoneAt.Equal(august) {
		t.Fatalf("trim-after-flowering last event = %+v, ok=%v, want August", got, ok)
	}
}

func TestLastTaskEventLegacyEventMatchesByKind(t *testing.T) {
	// An event logged before tasks had identity carries no TaskSlug; it must
	// still satisfy a task of its kind rather than being invisible to it.
	legacy := time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)
	events := []domain.CareEvent{{Kind: domain.Prune, TaskSlug: nil, DoneAt: legacy}}

	task := domain.SpeciesTask{Slug: "prune-hard", Kind: domain.Prune, Label: "Hard prune", IntervalDays: 365}
	got, ok := lastTaskEvent(events, task)
	if !ok || !got.DoneAt.Equal(legacy) {
		t.Fatalf("legacy match = %+v, ok=%v, want %s", got, ok, legacy)
	}
}

func TestLastTaskEventLegacyEventSatisfiesEveryTaskOfItsKind(t *testing.T) {
	// The documented transitional consequence: until each of lavender's two
	// prune tasks has its own logged event, one legacy "prune" event satisfies
	// both. This is deliberate — guessing which task it meant would be worse.
	legacy := time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)
	events := []domain.CareEvent{{Kind: domain.Prune, TaskSlug: nil, DoneAt: legacy}}

	hard := domain.SpeciesTask{Slug: "prune-hard", Kind: domain.Prune, Label: "Hard prune", IntervalDays: 365}
	trim := domain.SpeciesTask{Slug: "trim-after-flowering", Kind: domain.Prune, Label: "Trim", IntervalDays: 365}
	for _, task := range []domain.SpeciesTask{hard, trim} {
		got, ok := lastTaskEvent(events, task)
		if !ok || !got.DoneAt.Equal(legacy) {
			t.Fatalf("task %s: match = %+v, ok=%v, want the shared legacy event", task.Slug, got, ok)
		}
	}
}

func TestLastTaskEventASlugStopsMatchingOnceOwnEventExists(t *testing.T) {
	// Once prune-hard gets its own real event, the legacy event must no
	// longer bleed into it — but must still satisfy trim, which has none yet.
	legacy := time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)
	hardSlug := "prune-hard"
	realEvent := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	events := []domain.CareEvent{
		{Kind: domain.Prune, TaskSlug: nil, DoneAt: legacy},
		{Kind: domain.Prune, TaskSlug: &hardSlug, DoneAt: realEvent},
	}

	hard := domain.SpeciesTask{Slug: hardSlug, Kind: domain.Prune, Label: "Hard prune", IntervalDays: 365}
	got, ok := lastTaskEvent(events, hard)
	if !ok || !got.DoneAt.Equal(realEvent) {
		t.Fatalf("prune-hard = %+v, ok=%v, want its own real event", got, ok)
	}

	trim := domain.SpeciesTask{Slug: "trim-after-flowering", Kind: domain.Prune, Label: "Trim", IntervalDays: 365}
	got, ok = lastTaskEvent(events, trim)
	if !ok || !got.DoneAt.Equal(legacy) {
		t.Fatalf("trim-after-flowering = %+v, ok=%v, want the legacy event", got, ok)
	}
}

func TestLastTaskEventIgnoresVoided(t *testing.T) {
	slug := "prune"
	voided := time.Now()
	events := []domain.CareEvent{{Kind: domain.Prune, TaskSlug: &slug, DoneAt: time.Now(), VoidedAt: &voided}}
	task := domain.SpeciesTask{Slug: slug, Kind: domain.Prune, Label: "Prune", IntervalDays: 30}
	if _, ok := lastTaskEvent(events, task); ok {
		t.Fatal("voided event should not satisfy a task")
	}
}
