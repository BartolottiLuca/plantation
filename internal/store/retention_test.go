package store

import (
	"context"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestPurgeOlderThanRemovesTelemetryKeepsCareEvents(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	species := NewSpeciesRepo(pool)
	plants := NewPlantRepo(pool)
	events := NewCareEventRepo(pool)
	weather := NewWeatherRepo(pool)
	climate := NewClimateRepo(pool)
	notes := NewNotificationRepo(pool)
	retain := NewRetentionRepo(pool)
	mustSpecies(t, species, "monstera-deliciosa")

	p, err := plants.Create(ctx, domain.Plant{
		Name:          "Hallway",
		SpeciesSlug:   "monstera-deliciosa",
		Location:      domain.Indoor,
		PotDiameterMM: 180,
		FExposure:     1,
		Active:        true,
	})
	if err != nil {
		t.Fatalf("Create plant: %v", err)
	}

	loc := "51.500,-0.120"
	oldDay := domain.Date{Year: 2026, Month: time.August, Day: 18}
	keepDay := domain.Date{Year: 2026, Month: time.August, Day: 20}
	et0 := 2.0
	if err := weather.UpsertDays(ctx, []WeatherDay{
		{LocationKey: loc, Date: oldDay, Kind: WeatherObserved, ET0MM: &et0},
		{LocationKey: loc, Date: keepDay, Kind: WeatherObserved, ET0MM: &et0},
	}); err != nil {
		t.Fatalf("UpsertDays: %v", err)
	}

	oldSample := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	keepSample := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	if err := climate.AddSamples(ctx, []ClimateSample{
		{RoomID: "living", ObservedAt: oldSample, TempC: 21, HumidityPct: 50},
		{RoomID: "living", ObservedAt: keepSample, TempC: 22, HumidityPct: 51},
	}); err != nil {
		t.Fatalf("AddSamples: %v", err)
	}

	if _, err := notes.Claim(ctx, "digest:2026-08-18", "digest"); err != nil {
		t.Fatalf("Claim old: %v", err)
	}
	if _, err := notes.Claim(ctx, "digest:2026-08-20", "digest"); err != nil {
		t.Fatalf("Claim keep: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notifications SET claimed_at = $1 WHERE dedupe_key = $2`,
		oldSample, "digest:2026-08-18"); err != nil {
		t.Fatalf("backdate notification: %v", err)
	}

	doneAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	if _, err := events.Add(ctx, domain.CareEvent{
		PlantID: p.ID, Kind: domain.Water, DoneAt: doneAt, Source: "web",
	}); err != nil {
		t.Fatalf("Add event: %v", err)
	}

	before := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	if err := retain.PurgeOlderThan(ctx, before); err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}

	var weatherN int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM weather_daily`).Scan(&weatherN); err != nil {
		t.Fatal(err)
	}
	if weatherN != 1 {
		t.Fatalf("weather rows = %d, want 1", weatherN)
	}
	var climateN int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM room_climate_samples`).Scan(&climateN); err != nil {
		t.Fatal(err)
	}
	if climateN != 1 {
		t.Fatalf("climate rows = %d, want 1", climateN)
	}
	var noteN int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&noteN); err != nil {
		t.Fatal(err)
	}
	if noteN != 1 {
		t.Fatalf("notification rows = %d, want 1", noteN)
	}
	var eventN int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM care_events`).Scan(&eventN); err != nil {
		t.Fatal(err)
	}
	if eventN != 1 {
		t.Fatalf("care_events = %d, want 1 (never purged)", eventN)
	}
}
