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
		PotDiameterMM: 180,
		FExposure:     1.0,
		Active:        true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID.String() == "" || !p.Active {
		t.Fatalf("created plant = %+v", p)
	}

	if err := tasks.Enable(ctx, p.ID, domain.Water); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	until := domain.Date{Year: 2026, Month: time.September, Day: 20}
	if err := tasks.Snooze(ctx, p.ID, domain.Water, &until); err != nil {
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
	if !got.InGround || got.PotDiameterMM != 0 {
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
			PotDiameterMM: 180, FExposure: 1.0, Active: true,
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
	if err := tasks.Snooze(ctx, ids[2], domain.Prune, &until); err != nil {
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
