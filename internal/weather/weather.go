// Package weather fetches outdoor daily weather for the care engine.
package weather

import (
	"context"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

const (
	KindObserved = "observed"
	KindForecast = "forecast"
)

type DailyWeather struct {
	Date       domain.Date
	Kind       string
	ET0MM      *float64
	PrecipMM   *float64
	PrecipProb *float64
	TMinC      *float64
	TMaxC      *float64
	FetchedAt  time.Time
}

type WeatherProvider interface {
	// Daily returns ascending days covering past pastDays and the forecast horizon.
	Daily(ctx context.Context, lat, lon float64, pastDays int) ([]DailyWeather, error)
}

type NoopWeather struct{}

func (*NoopWeather) Daily(context.Context, float64, float64, int) ([]DailyWeather, error) {
	return nil, nil
}

var _ WeatherProvider = (*NoopWeather)(nil)
