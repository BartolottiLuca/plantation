package store

import (
	"context"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestForecastDoesNotOverwriteObservedForPastDate(t *testing.T) {
	pool := migratedPool(t)
	repo := NewWeatherRepo(pool)
	ctx := context.Background()

	past := domain.Date{Year: 2026, Month: time.September, Day: 10}
	loc := "51.500,-0.120"
	et0Obs := 3.1
	et0Fc := 9.9
	precipObs := 1.2

	if err := repo.UpsertDays(ctx, []WeatherDay{{
		LocationKey: loc,
		Date:        past,
		Kind:        WeatherObserved,
		ET0MM:       &et0Obs,
		PrecipMM:    &precipObs,
	}}); err != nil {
		t.Fatalf("upsert observed: %v", err)
	}
	if err := repo.UpsertDays(ctx, []WeatherDay{{
		LocationKey: loc,
		Date:        past,
		Kind:        WeatherForecast,
		ET0MM:       &et0Fc,
	}}); err != nil {
		t.Fatalf("upsert forecast: %v", err)
	}

	var kind string
	var et0 float64
	err := pool.QueryRow(ctx, `
		SELECT kind, et0_mm FROM weather_daily
		WHERE location_key = $1 AND date = $2 AND kind = 'observed'`,
		loc, civilDate(past)).Scan(&kind, &et0)
	if err != nil {
		t.Fatalf("select observed: %v", err)
	}
	if kind != WeatherObserved || et0 != et0Obs {
		t.Fatalf("observed row became kind=%s et0=%v, want observed %v", kind, et0, et0Obs)
	}

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM weather_daily WHERE location_key = $1 AND date = $2`,
		loc, civilDate(past)).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("row count for date = %d, want 2 (observed + forecast)", n)
	}
}

func TestRangePrefersObserved(t *testing.T) {
	pool := migratedPool(t)
	repo := NewWeatherRepo(pool)
	ctx := context.Background()

	day := domain.Date{Year: 2026, Month: time.September, Day: 11}
	loc := "51.500,-0.120"
	obs := 2.5
	fc := 4.0

	if err := repo.UpsertDays(ctx, []WeatherDay{
		{LocationKey: loc, Date: day, Kind: WeatherForecast, ET0MM: &fc},
		{LocationKey: loc, Date: day, Kind: WeatherObserved, ET0MM: &obs},
	}); err != nil {
		t.Fatalf("UpsertDays: %v", err)
	}

	got, err := repo.Range(ctx, loc, day, day)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Range len = %d, want 1", len(got))
	}
	if got[0].Kind != WeatherObserved {
		t.Fatalf("Range kind = %q, want observed", got[0].Kind)
	}
	if got[0].ET0MM == nil || *got[0].ET0MM != obs {
		t.Fatalf("Range ET0 = %v, want %v", got[0].ET0MM, obs)
	}
}
