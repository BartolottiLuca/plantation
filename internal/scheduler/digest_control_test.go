package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

type fakeTasks struct {
	rows map[uuid.UUID][]store.CareTask
	err  error
}

func (f *fakeTasks) ListForPlants(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]store.CareTask, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID][]store.CareTask{}
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

var controlPlantID = uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

// digestLoop parks the clock just past the digest hour with one long-overdue
// outdoor plant, so a digest is owed unless a control suppresses it. An empty
// day is recorded as "skipped" rather than sent, which is what digestSent
// filters on — so its absence there is the assertion.
func digestLoop(t *testing.T, tasks TaskLister) (*Loop, *fakeNotify) {
	t.Helper()
	loc := london(t)
	watered := time.Date(2026, 6, 1, 9, 0, 0, 0, loc)
	n := newFakeNotify()
	loop := New(Loop{
		Clock:      &testClock{t: time.Date(2026, 7, 1, 10, 0, 0, 0, loc)},
		Location:   loc,
		DigestHour: 9,
		BaseURL:    "https://plantation.example.invalid",
		Lock:       AlwaysLock{},
		Plants:     &fakePlants{rows: []store.PlantWithSpecies{plantRow(controlPlantID, watered)}},
		Events:     &fakeEvents{byPlant: map[uuid.UUID][]domain.CareEvent{}},
		Tasks:      tasks,
		Notify:     n,
		Log:        discardLog(),
	})
	return loop, n
}

func waterControl(t store.CareTask) TaskLister {
	t.PlantID = controlPlantID
	t.TaskSlug = domain.WaterSlug
	return &fakeTasks{rows: map[uuid.UUID][]store.CareTask{controlPlantID: {t}}}
}

// The README promises the dashboard and the digest can never disagree. The web
// side is pinned in internal/web; this is the digest half of the same contract.
func TestDigestHonoursTaskControls(t *testing.T) {
	const key = "digest:2026-07-01"
	aug1 := domain.Date{Year: 2026, Month: time.August, Day: 1}
	jun1 := domain.Date{Year: 2026, Month: time.June, Day: 1}

	tests := []struct {
		name  string
		tasks TaskLister
		want  bool // a real digest, as opposed to a recorded empty day
	}{
		{"no control rows at all", nil, true},
		{"enabled", waterControl(store.CareTask{Enabled: true}), true},
		{"disabled", waterControl(store.CareTask{Enabled: false}), false},
		{"snoozed into the future", waterControl(store.CareTask{Enabled: true, SnoozedUntil: &aug1}), false},
		{"snooze already elapsed", waterControl(store.CareTask{Enabled: true, SnoozedUntil: &jun1}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loop, n := digestLoop(t, tc.tasks)
			loop.Tick(context.Background())

			sent := len(n.digestSent()) == 1
			if sent != tc.want {
				t.Fatalf("digest sent = %v, want %v (keys: %v)", sent, tc.want, n.keys("digest:"))
			}
			// Either way the day must be recorded, so the model is not
			// re-evaluated on every tick (SPEC §8).
			if !n.has(key) {
				t.Errorf("day not recorded at all; keys = %v", n.keys("digest:"))
			}
		})
	}
}

func TestDigestSurvivesATaskListerFailure(t *testing.T) {
	loop, n := digestLoop(t, &fakeTasks{err: context.DeadlineExceeded})
	loop.Tick(context.Background())
	if len(n.digestSent()) != 0 {
		t.Error("digest sent from task controls that failed to load")
	}
	// The tick must not panic and the loop must stay usable.
	loop.Tick(context.Background())
}

// SPEC §9: heatwave at tmax >= 32 C, one alert per forecast day however many
// times the forecast is polled.
func TestHeatwaveAlertOncePerForecastDay(t *testing.T) {
	loc := london(t)
	hot := domain.Date{Year: 2026, Month: time.July, Day: 2}
	mild := domain.Date{Year: 2026, Month: time.July, Day: 3}
	id := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	n := newFakeNotify()
	loop := New(Loop{
		Clock:      &testClock{t: time.Date(2026, 7, 1, 10, 0, 0, 0, loc)},
		Location:   loc,
		DigestHour: 9,
		BaseURL:    "https://plantation.example.invalid",
		Lock:       AlwaysLock{},
		Weather: &fakeWeather{series: care.EnvSeries{Days: []care.DayEnv{
			{Date: hot, TMaxC: 34, Observed: false},
			{Date: mild, TMaxC: 24, Observed: false},
		}}},
		Plants: &fakePlants{rows: []store.PlantWithSpecies{plantRow(id, time.Date(2026, 6, 28, 9, 0, 0, 0, loc))}},
		Events: &fakeEvents{byPlant: map[uuid.UUID][]domain.CareEvent{}},
		Notify: n,
		Log:    discardLog(),
	})

	for range 5 {
		loop.Tick(context.Background())
	}

	keys := n.keys("heatwave:")
	if len(keys) != 1 {
		t.Fatalf("heatwave keys = %v, want exactly one across 5 polls", keys)
	}
	if keys[0] != "heatwave:2026-07-02" {
		t.Errorf("key = %s, want the 34 C day rather than the 24 C one", keys[0])
	}
}

// TestDigestUsesTaskLabelsNotKindNames is C19's digest half of the two-tasks-
// of-one-kind requirement: with two prune tasks on one species, the digest
// line for each must say its own label, not "prune" twice.
func TestDigestUsesTaskLabelsNotKindNames(t *testing.T) {
	loc := london(t)
	id := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	sp := domain.Species{
		Slug: "lavandula-angustifolia", CommonName: "Lavender", Placement: domain.Outdoor,
		Kc: 0.4, Substrate: domain.Cactus, MAD: 0.7,
		BaseIntervalDays: 8, MinIntervalDays: 4, MaxIntervalDays: 21,
		Tasks: []domain.SpeciesTask{
			{Slug: "spring-tidy", Kind: domain.Prune, Label: "Tidy after winter", IntervalDays: 3},
			{Slug: "prune-after-flowering", Kind: domain.Prune, Label: "Cut back after flowering", IntervalDays: 5},
		},
	}
	row := store.PlantWithSpecies{
		Plant: domain.Plant{
			ID: id, Name: "Bed Lavender", SpeciesSlug: sp.Slug, Location: domain.Outdoor,
			InGround: true, FExposure: 1, FRain: 0.9, Active: true,
		},
		Species: sp,
	}
	// A fresh fixed task with no prior event is never due today or overdue —
	// its earliest possible date is IntervalDays out (Status upcoming), and
	// the digest only ever includes overdue/due-today. Anchor each task to a
	// past event so both land squarely in the digest.
	tidySlug, cutSlug := "spring-tidy", "prune-after-flowering"
	tidyDone := time.Date(2026, 6, 26, 9, 0, 0, 0, loc) // +3d = 2026-06-29, overdue
	cutDone := time.Date(2026, 6, 25, 9, 0, 0, 0, loc)  // +5d = 2026-06-30, overdue
	events := map[uuid.UUID][]domain.CareEvent{
		id: {
			{PlantID: id, Kind: domain.Prune, TaskSlug: &tidySlug, DoneAt: tidyDone},
			{PlantID: id, Kind: domain.Prune, TaskSlug: &cutSlug, DoneAt: cutDone},
		},
	}
	n := newFakeNotify()
	loop := New(Loop{
		Clock:      &testClock{t: time.Date(2026, 7, 1, 10, 0, 0, 0, loc)},
		Location:   loc,
		DigestHour: 9,
		BaseURL:    "https://plantation.example.invalid",
		Lock:       AlwaysLock{},
		Plants:     &fakePlants{rows: []store.PlantWithSpecies{row}},
		Events:     &fakeEvents{byPlant: events},
		Notify:     n,
		Log:        discardLog(),
	})
	loop.Tick(context.Background())

	sent := n.digestSent()
	if len(sent) != 1 {
		t.Fatalf("digest keys = %v, want exactly one", sent)
	}
	body := n.bodyOf(sent[0]).Description
	if !strings.Contains(body, "Tidy after winter") || !strings.Contains(body, "Cut back after flowering") {
		t.Fatalf("digest body missing one or both task labels:\n%s", body)
	}
	if strings.Contains(body, "— prune —") {
		t.Errorf("digest fell back to the generic kind name instead of the task label:\n%s", body)
	}
}
