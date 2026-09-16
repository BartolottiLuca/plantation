package weather

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
)

const (
	// staleAfter matches SPEC.md §10.1's degradation ladder: a cache older
	// than this is treated as down, not just imprecise.
	staleAfter = 48 * time.Hour
	// trailingMeanDays is the width of the observed-ET0 window used to fill
	// gaps once the cache is stale.
	trailingMeanDays = 14
	// refreshPastDays is the past_days value the normal (non-backfill) fetch
	// always uses; SPEC.md §10.1 calls this out explicitly because it is what
	// lets a week-long outage self-heal without a separate recovery path.
	refreshPastDays = 7
)

// Store is the subset of *store.WeatherRepo this package depends on. Keeping
// it local (rather than taking *store.WeatherRepo directly) means tests can
// substitute an in-memory fake with no Postgres pool, while the real repo
// satisfies this interface structurally with no adapter needed.
type Store interface {
	UpsertDays(ctx context.Context, days []store.WeatherDay) error
	Range(ctx context.Context, locationKey string, from, to domain.Date) ([]store.WeatherDay, error)
	LastFetchedAt(ctx context.Context, locationKey string) (time.Time, error)
}

// Service is the storage-backed weather source the care engine reads from.
// It owns one location; cmd wiring constructs one per configured coordinate.
type Service struct {
	Provider WeatherProvider
	Store    Store
	Lat      float64
	Lon      float64
	// Clock supplies "now" for staleness comparisons and for choosing the
	// trailing-mean window in Series. Refresh never reads the clock: rows are
	// written with FetchedAt left zero, and the store's upsert defaults
	// fetched_at to the database's own now() — one clock of record instead
	// of two that can drift.
	Clock care.Clock
	// Log is optional. If set, only the rounded location_key is ever logged,
	// never raw coordinates (AGENTS.md).
	Log *slog.Logger
}

// NewService builds a Service for one coordinate.
func NewService(provider WeatherProvider, st Store, lat, lon float64, clock care.Clock, log *slog.Logger) *Service {
	return &Service{Provider: provider, Store: st, Lat: lat, Lon: lon, Clock: clock, Log: log}
}

func locationKey(lat, lon float64) string {
	return fmt.Sprintf("%.3f,%.3f", lat, lon)
}

// Refresh fetches the current forecast (with SPEC.md §10.1's past_days=7)
// and upserts it into the cache. It is the scheduler's job to decide when to
// call this; this method just does the one fetch-and-store.
func (s *Service) Refresh(ctx context.Context) error {
	key := locationKey(s.Lat, s.Lon)

	days, err := s.Provider.Daily(ctx, s.Lat, s.Lon, refreshPastDays)
	if err != nil {
		return fmt.Errorf("fetching weather for %s: %w", key, err)
	}
	if len(days) == 0 {
		return nil
	}

	rows := toWeatherDays(key, days)
	if err := s.Store.UpsertDays(ctx, rows); err != nil {
		return fmt.Errorf("caching weather for %s: %w", key, err)
	}
	if s.Log != nil {
		s.Log.Info("weather refreshed", "location_key", key, "days", len(rows))
	}
	return nil
}

func toWeatherDays(key string, days []DailyWeather) []store.WeatherDay {
	rows := make([]store.WeatherDay, 0, len(days))
	for _, d := range days {
		rows = append(rows, store.WeatherDay{
			LocationKey: key,
			Date:        d.Date,
			Kind:        d.Kind,
			ET0MM:       d.ET0MM,
			PrecipMM:    d.PrecipMM,
			PrecipProb:  d.PrecipProb,
			TMinC:       d.TMinC,
			TMaxC:       d.TMaxC,
			FetchedAt:   d.FetchedAt,
		})
	}
	return rows
}

// Series reads the cache for [from, to] and assembles a care.EnvSeries,
// applying the degradation ladder: a cache fresher than 48h is used as-is;
// once it is older than that (or has never been fetched), any day in the
// requested range that the cache does not have is filled with the trailing
// 14-day mean of observed ET0 and zero rain, flagged Estimated. An empty or
// fully-estimated result is valid output — only a genuine store failure is
// an error.
func (s *Service) Series(ctx context.Context, from, to domain.Date) (care.EnvSeries, error) {
	key := locationKey(s.Lat, s.Lon)

	cached, err := s.Store.Range(ctx, key, from, to)
	if err != nil {
		return care.EnvSeries{}, fmt.Errorf("reading cached weather for %s: %w", key, err)
	}
	lastFetched, err := s.Store.LastFetchedAt(ctx, key)
	if err != nil {
		return care.EnvSeries{}, fmt.Errorf("reading weather freshness for %s: %w", key, err)
	}

	now := s.now()
	stale := lastFetched.IsZero() || now.Sub(lastFetched) > staleAfter

	byDate := make(map[string]store.WeatherDay, len(cached))
	for _, row := range cached {
		byDate[row.Date.String()] = row
	}

	var meanET0 float64
	var haveMean bool
	if stale {
		today := domain.TodayIn(now, time.UTC)
		meanET0, haveMean, err = s.trailingObservedMeanET0(ctx, key, today)
		if err != nil {
			return care.EnvSeries{}, err
		}
	}

	var out []care.DayEnv
	for d := from; !to.Before(d); d = d.AddDays(1) {
		row, cachedDay := byDate[d.String()]
		switch {
		case cachedDay && row.ET0MM != nil:
			out = append(out, dayEnvFromRow(d, row))
		case cachedDay:
			// The row exists but ET0 is nil (Open-Meteo returned null for
			// this day). care.DayEnv has no per-field missing flag, so a
			// fabricated 0.0 here would look like real zero-ET0 data to the
			// water-balance model. Drop the day instead of lying about it.
			continue
		case stale && haveMean:
			out = append(out, care.DayEnv{Date: d, ET0MM: meanET0, Estimated: true})
		default:
			continue
		}
	}

	return care.EnvSeries{Days: out, OutdoorStale: stale}, nil
}

func dayEnvFromRow(d domain.Date, row store.WeatherDay) care.DayEnv {
	day := care.DayEnv{
		Date:     d,
		ET0MM:    *row.ET0MM,
		Observed: row.Kind == store.WeatherObserved,
	}
	if row.PrecipMM != nil {
		day.PrecipMM = *row.PrecipMM
	}
	if row.PrecipProb != nil {
		day.PrecipProb = *row.PrecipProb
	}
	if row.TMinC != nil {
		day.TMinC = *row.TMinC
	}
	if row.TMaxC != nil {
		day.TMaxC = *row.TMaxC
	}
	return day
}

func (s *Service) trailingObservedMeanET0(ctx context.Context, key string, today domain.Date) (float64, bool, error) {
	rows, err := s.Store.Range(ctx, key, today.AddDays(-trailingMeanDays), today.AddDays(-1))
	if err != nil {
		return 0, false, fmt.Errorf("reading trailing weather for %s: %w", key, err)
	}
	var sum float64
	n := 0
	for _, row := range rows {
		if row.Kind != store.WeatherObserved || row.ET0MM == nil {
			continue
		}
		sum += *row.ET0MM
		n++
	}
	if n == 0 {
		return 0, false, nil
	}
	return sum / float64(n), true, nil
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Time{}
	}
	return s.Clock.Now()
}

// RunBackfill does a one-off wider fetch (wideDays of history instead of the
// normal refreshPastDays) and upserts it, for recovering after an outage
// longer than SPEC.md §10.1's self-healing week. It takes its dependencies
// as arguments rather than a *Service so cmd can wire it as a standalone
// subcommand without constructing a full Service.
func RunBackfill(ctx context.Context, provider WeatherProvider, st Store, lat, lon float64, wideDays int) error {
	key := locationKey(lat, lon)

	days, err := provider.Daily(ctx, lat, lon, wideDays)
	if err != nil {
		return fmt.Errorf("backfilling weather for %s: %w", key, err)
	}
	if len(days) == 0 {
		return nil
	}

	if err := st.UpsertDays(ctx, toWeatherDays(key, days)); err != nil {
		return fmt.Errorf("caching backfilled weather for %s: %w", key, err)
	}
	return nil
}
