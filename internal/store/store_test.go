package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
)

func TestSpeciesPlantAndCareRoundTrip(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	species := NewSpeciesRepo(pool)
	plants := NewPlantRepo(pool)
	tasks := NewCareTaskRepo(pool)
	events := NewCareEventRepo(pool)
	climate := NewClimateRepo(pool)

	mustSpecies(t, species, "monstera-deliciosa")
	got, err := species.Get(ctx, "monstera-deliciosa")
	if err != nil {
		t.Fatalf("Get species: %v", err)
	}
	if got.CommonName == "" || got.Retired {
		t.Fatalf("species = %+v", got)
	}

	p, err := plants.Create(ctx, domain.Plant{
		Name:          "Hallway Monstera",
		SpeciesSlug:   "monstera-deliciosa",
		Location:      domain.Indoor,
		Place:         "hall",
		PotDiameterCM: 18,
		FExposure:     1.0,
		Active:        true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID.String() == "" || !p.Active {
		t.Fatalf("created plant = %+v", p)
	}

	if err := tasks.Enable(ctx, p.ID, domain.WaterSlug); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	until := domain.Date{Year: 2026, Month: time.September, Day: 20}
	if err := tasks.Snooze(ctx, p.ID, domain.WaterSlug, &until); err != nil {
		t.Fatalf("Snooze: %v", err)
	}
	list, err := tasks.List(ctx, p.ID)
	if err != nil || len(list) != 1 || !list[0].Enabled || list[0].SnoozedUntil == nil {
		t.Fatalf("List tasks = %+v err=%v", list, err)
	}

	ev, err := events.Add(ctx, domain.CareEvent{
		PlantID: p.ID,
		Kind:    domain.Water,
		DoneAt:  time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC),
		Note:    "soaked",
		Source:  "web",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	latest, err := events.LatestByKind(ctx, p.ID)
	if err != nil || len(latest) != 1 || latest[0].ID != ev.ID {
		t.Fatalf("LatestByKind = %+v err=%v", latest, err)
	}
	if err := events.Void(ctx, ev.ID); err != nil {
		t.Fatalf("Void: %v", err)
	}
	latest, err = events.LatestByKind(ctx, p.ID)
	if err != nil || len(latest) != 0 {
		t.Fatalf("LatestByKind after void = %+v err=%v", latest, err)
	}

	if err := plants.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	gone, err := plants.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if gone.Active {
		t.Fatal("soft delete left plant active")
	}

	joined, err := plants.List(ctx)
	if err != nil || len(joined) != 1 || joined[0].Species.Slug != "monstera-deliciosa" {
		t.Fatalf("List joined = %+v err=%v", joined, err)
	}

	room := "living"
	if err := climate.AddSamples(ctx, []ClimateSample{{
		RoomID: room, ObservedAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC),
		TempC: 21.5, HumidityPct: 48,
	}}); err != nil {
		t.Fatalf("AddSamples: %v", err)
	}
	sample, err := climate.LatestSample(ctx, room)
	if err != nil || sample.TempC != 21.5 {
		t.Fatalf("LatestSample = %+v err=%v", sample, err)
	}
	means, err := climate.DailyMeans(ctx, room,
		domain.Date{Year: 2026, Month: time.September, Day: 11},
		domain.Date{Year: 2026, Month: time.September, Day: 11})
	if err != nil || len(means) != 1 {
		t.Fatalf("DailyMeans = %+v err=%v", means, err)
	}

	_, err = species.Get(ctx, "missing-slug")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing species err = %v, want ErrNotFound", err)
	}
}

func TestInGroundPlantStoresNullPotDiameter(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	species := NewSpeciesRepo(pool)
	plants := NewPlantRepo(pool)
	mustSpecies(t, species, "monstera-deliciosa")

	p, err := plants.Create(ctx, domain.Plant{
		Name:        "Bed lavender",
		SpeciesSlug: "monstera-deliciosa",
		Location:    domain.Outdoor,
		InGround:    true,
		FExposure:   1.3,
		FRain:       0.9,
		Active:      true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := plants.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.InGround || got.PotDiameterCM != 0 {
		t.Fatalf("got %+v", got)
	}
	listed, err := plants.List(ctx)
	if err != nil || len(listed) != 1 || !listed[0].Plant.InGround {
		t.Fatalf("List = %+v err=%v", listed, err)
	}
}

func TestInGroundIndoorRejectedByCheck(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	species := NewSpeciesRepo(pool)
	plants := NewPlantRepo(pool)
	mustSpecies(t, species, "monstera-deliciosa")

	_, err := plants.Create(ctx, domain.Plant{
		Name:        "Indoor bed",
		SpeciesSlug: "monstera-deliciosa",
		Location:    domain.Indoor,
		InGround:    true,
		FExposure:   1.0,
		Active:      true,
	})
	if err == nil {
		t.Fatal("Create indoor in-ground succeeded")
	}
}

// The batch reads back the dashboard and the digest: one query for every plant
// rather than one per plant. Exercised against real Postgres because the
// DISTINCT ON and = ANY($1) forms are what a fake cannot check.
func TestBatchReadsMatchPerPlantReads(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	species := NewSpeciesRepo(pool)
	plants := NewPlantRepo(pool)
	tasks := NewCareTaskRepo(pool)
	events := NewCareEventRepo(pool)

	mustSpecies(t, species, "monstera-deliciosa")

	var ids []uuid.UUID
	for _, name := range []string{"Batch A", "Batch B", "Batch C"} {
		p, err := plants.Create(ctx, domain.Plant{
			Name: name, SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
			PotDiameterCM: 18, FExposure: 1.0, Active: true,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		ids = append(ids, p.ID)
	}

	// Two waterings on the first plant so DISTINCT ON has to pick the newer,
	// plus a voided one that must never surface.
	older := time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, time.September, 10, 9, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{older, newer} {
		if _, err := events.Add(ctx, domain.CareEvent{
			PlantID: ids[0], Kind: domain.Water, DoneAt: at, Source: "cli",
		}); err != nil {
			t.Fatalf("Add event: %v", err)
		}
	}
	voided, err := events.Add(ctx, domain.CareEvent{
		PlantID: ids[1], Kind: domain.Water,
		DoneAt: time.Date(2026, time.September, 5, 9, 0, 0, 0, time.UTC),
		Source: "cli",
	})
	if err != nil {
		t.Fatalf("Add voided event: %v", err)
	}
	if err := events.Void(ctx, voided.ID); err != nil {
		t.Fatalf("Void: %v", err)
	}

	until := domain.Date{Year: 2026, Month: time.October, Day: 1}
	if err := tasks.Snooze(ctx, ids[2], "prune", &until); err != nil {
		t.Fatalf("Snooze: %v", err)
	}

	gotEvents, err := events.LatestByKindForPlants(ctx, ids)
	if err != nil {
		t.Fatalf("LatestByKindForPlants: %v", err)
	}
	gotTasks, err := tasks.ListForPlants(ctx, ids)
	if err != nil {
		t.Fatalf("ListForPlants: %v", err)
	}

	// Batch and per-plant must agree for every plant; that equivalence is the
	// whole contract, since the two are used interchangeably.
	for _, id := range ids {
		wantEvents, err := events.LatestByKind(ctx, id)
		if err != nil {
			t.Fatalf("LatestByKind: %v", err)
		}
		if len(gotEvents[id]) != len(wantEvents) {
			t.Errorf("plant %s: batch returned %d events, per-plant %d", id, len(gotEvents[id]), len(wantEvents))
		}
		wantTasks, err := tasks.List(ctx, id)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(gotTasks[id]) != len(wantTasks) {
			t.Errorf("plant %s: batch returned %d tasks, per-plant %d", id, len(gotTasks[id]), len(wantTasks))
		}
	}

	if n := len(gotEvents[ids[0]]); n != 1 {
		t.Fatalf("plant A has %d water events, want 1 (DISTINCT ON should collapse)", n)
	}
	if got := gotEvents[ids[0]][0].DoneAt; !got.Equal(newer) {
		t.Errorf("DoneAt = %s, want the newer %s", got, newer)
	}
	if _, ok := gotEvents[ids[1]]; ok {
		t.Error("voided event surfaced in the batch read")
	}
	if n := len(gotTasks[ids[2]]); n != 1 {
		t.Errorf("plant C has %d task rows, want 1", n)
	}

	empty, err := events.LatestByKindForPlants(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty id list = %v, %v; want an empty map and no error", empty, err)
	}
}

// TestSpeciesTasksRoundTripAndOrdering exercises UpsertAll/List/Get with a
// real task list, including a species with two tasks of the same kind —
// lavender's hard spring prune and light after-flowering trim — which the
// old three-column shape could never express.
func TestSpeciesTasksRoundTripAndOrdering(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	species := NewSpeciesRepo(pool)

	lavender := domain.Species{
		Slug: "lavandula-angustifolia", CommonName: "English lavender",
		Description: "Prefers poor, sharply-draining soil and full sun.",
		Placement:   domain.Outdoor, Kc: 0.4, Substrate: domain.Cactus, MAD: 0.7,
		BaseIntervalDays: 8, MinIntervalDays: 4, MaxIntervalDays: 21, MinTempC: -15,
		Tasks: []domain.SpeciesTask{
			{Slug: "prune-hard", Kind: domain.Prune, Label: "Hard prune",
				IntervalDays: 365, ActiveMonths: []time.Month{time.March}},
			{Slug: "trim-after-flowering", Kind: domain.Prune, Label: "Trim after flowering",
				IntervalDays: 365, ActiveMonths: []time.Month{time.August}},
			{Slug: "repot", Kind: domain.Repot, Label: "Repot",
				IntervalDays: 1095},
		},
	}
	if err := species.UpsertAll(ctx, []domain.Species{lavender}); err != nil {
		t.Fatalf("UpsertAll: %v", err)
	}

	got, err := species.Get(ctx, "lavandula-angustifolia")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != lavender.Description {
		t.Errorf("Description = %q, want %q", got.Description, lavender.Description)
	}
	if len(got.Tasks) != 3 {
		t.Fatalf("Tasks = %+v, want 3", got.Tasks)
	}
	// Insertion order (sort_order) must survive the round trip: two tasks of
	// the same kind are only distinguishable by slug and position.
	if got.Tasks[0].Slug != "prune-hard" || got.Tasks[1].Slug != "trim-after-flowering" {
		t.Errorf("task order = [%s, %s], want [prune-hard, trim-after-flowering]",
			got.Tasks[0].Slug, got.Tasks[1].Slug)
	}
	if got.Tasks[0].Kind != domain.Prune || got.Tasks[1].Kind != domain.Prune {
		t.Errorf("both tasks should be kind=prune: %+v", got.Tasks)
	}

	list, err := species.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var listed domain.Species
	for _, s := range list {
		if s.Slug == "lavandula-angustifolia" {
			listed = s
		}
	}
	if len(listed.Tasks) != 3 {
		t.Fatalf("List: Tasks = %+v, want 3 (batch attach failed)", listed.Tasks)
	}

	// Re-upserting with a shrunk task list must remove the dropped task, not
	// leave it behind — species_tasks is rebuilt wholesale, like species itself.
	lavender.Tasks = lavender.Tasks[:1]
	if err := species.UpsertAll(ctx, []domain.Species{lavender}); err != nil {
		t.Fatalf("UpsertAll (shrink): %v", err)
	}
	got, err = species.Get(ctx, "lavandula-angustifolia")
	if err != nil {
		t.Fatalf("Get after shrink: %v", err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Slug != "prune-hard" {
		t.Fatalf("Tasks after shrink = %+v, want only prune-hard", got.Tasks)
	}
}

// TestTwoTasksOfOneKindTrackIndependentLastDone is the store-layer proof for
// the reason care_events.task_slug and the (kind, task_slug) grouping exist:
// lavender's two prune tasks must not collapse onto a single "last pruned"
// date the way a plain DISTINCT ON (kind) would.
func TestTwoTasksOfOneKindTrackIndependentLastDone(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	speciesRepo := NewSpeciesRepo(pool)
	plants := NewPlantRepo(pool)
	events := NewCareEventRepo(pool)

	mustSpecies(t, speciesRepo, "lavandula-angustifolia")
	p, err := plants.Create(ctx, domain.Plant{
		Name: "Bed lavender", SpeciesSlug: "lavandula-angustifolia",
		Location: domain.Outdoor, InGround: true, FExposure: 1.0, FRain: 0.9, Active: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	hardSlug := "prune-hard"
	trimSlug := "trim-after-flowering"
	march := time.Date(2026, time.March, 15, 9, 0, 0, 0, time.UTC)
	august := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)
	if _, err := events.Add(ctx, domain.CareEvent{
		PlantID: p.ID, Kind: domain.Prune, TaskSlug: &hardSlug, DoneAt: march, Source: "web",
	}); err != nil {
		t.Fatalf("Add hard prune: %v", err)
	}
	if _, err := events.Add(ctx, domain.CareEvent{
		PlantID: p.ID, Kind: domain.Prune, TaskSlug: &trimSlug, DoneAt: august, Source: "web",
	}); err != nil {
		t.Fatalf("Add trim: %v", err)
	}

	latest, err := events.LatestByKind(ctx, p.ID)
	if err != nil {
		t.Fatalf("LatestByKind: %v", err)
	}
	if len(latest) != 2 {
		t.Fatalf("LatestByKind returned %d events, want 2 (one per task, not one per kind): %+v",
			len(latest), latest)
	}
	bySlug := map[string]domain.CareEvent{}
	for _, e := range latest {
		if e.TaskSlug == nil {
			t.Fatalf("event with nil TaskSlug in a fully-slugged pair: %+v", e)
		}
		bySlug[*e.TaskSlug] = e
	}
	if !bySlug[hardSlug].DoneAt.Equal(march) {
		t.Errorf("%s last done %s, want %s", hardSlug, bySlug[hardSlug].DoneAt, march)
	}
	if !bySlug[trimSlug].DoneAt.Equal(august) {
		t.Errorf("%s last done %s, want %s", trimSlug, bySlug[trimSlug].DoneAt, august)
	}

	batch, err := events.LatestByKindForPlants(ctx, []uuid.UUID{p.ID})
	if err != nil {
		t.Fatalf("LatestByKindForPlants: %v", err)
	}
	if len(batch[p.ID]) != 2 {
		t.Fatalf("batch form returned %d events, want 2: %+v", len(batch[p.ID]), batch[p.ID])
	}
}
