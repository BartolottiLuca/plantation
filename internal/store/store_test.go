package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
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
