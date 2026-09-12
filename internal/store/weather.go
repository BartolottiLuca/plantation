package store

import (
	"context"
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WeatherRepo struct {
	pool *pgxpool.Pool
}

func NewWeatherRepo(pool *pgxpool.Pool) *WeatherRepo {
	return &WeatherRepo{pool: pool}
}

// UpsertDays writes days keyed on (location_key, date, kind). A forecast row
// therefore cannot replace a stored observed row for the same date.
func (r *WeatherRepo) UpsertDays(ctx context.Context, days []WeatherDay) error {
	return Retry(ctx, func(ctx context.Context) error {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("upserting weather: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		const q = `
			INSERT INTO weather_daily (
				location_key, date, kind, et0_mm, precip_mm, precip_prob, tmin_c, tmax_c, fetched_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9::timestamptz, now())
			)
			ON CONFLICT (location_key, date, kind) DO UPDATE SET
				et0_mm = EXCLUDED.et0_mm,
				precip_mm = EXCLUDED.precip_mm,
				precip_prob = EXCLUDED.precip_prob,
				tmin_c = EXCLUDED.tmin_c,
				tmax_c = EXCLUDED.tmax_c,
				fetched_at = EXCLUDED.fetched_at`

		for _, d := range days {
			var fetched any
			if !d.FetchedAt.IsZero() {
				fetched = d.FetchedAt
			}
			_, err := tx.Exec(ctx, q,
				d.LocationKey, civilDate(d.Date), d.Kind,
				d.ET0MM, d.PrecipMM, d.PrecipProb, d.TMinC, d.TMaxC, fetched,
			)
			if err != nil {
				return fmt.Errorf("upserting weather: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("upserting weather: %w", err)
		}
		return nil
	})
}

// Range returns one row per date in [from, to], preferring observed over forecast.
func (r *WeatherRepo) Range(ctx context.Context, locationKey string, from, to domain.Date) ([]WeatherDay, error) {
	var out []WeatherDay
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT DISTINCT ON (date)
				location_key, date, kind, et0_mm, precip_mm, precip_prob, tmin_c, tmax_c, fetched_at
			FROM weather_daily
			WHERE location_key = $1 AND date >= $2 AND date <= $3
			ORDER BY date ASC, CASE kind WHEN 'observed' THEN 0 ELSE 1 END`,
			locationKey, civilDate(from), civilDate(to))
		if err != nil {
			return fmt.Errorf("ranging weather: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			d, err := scanWeatherDay(rows)
			if err != nil {
				return fmt.Errorf("ranging weather: %w", err)
			}
			out = append(out, d)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("ranging weather: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []WeatherDay{}
	}
	return out, nil
}

func (r *WeatherRepo) LastFetchedAt(ctx context.Context, locationKey string) (time.Time, error) {
	var fetched *time.Time
	err := Retry(ctx, func(ctx context.Context) error {
		err := r.pool.QueryRow(ctx, `
			SELECT max(fetched_at) FROM weather_daily WHERE location_key = $1`, locationKey).Scan(&fetched)
		if err != nil {
			return fmt.Errorf("last weather fetch: %w", err)
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	if fetched == nil {
		return time.Time{}, nil
	}
	return *fetched, nil
}

func scanWeatherDay(row speciesScanner) (WeatherDay, error) {
	var (
		d    WeatherDay
		when time.Time
	)
	err := row.Scan(
		&d.LocationKey, &when, &d.Kind,
		&d.ET0MM, &d.PrecipMM, &d.PrecipProb, &d.TMinC, &d.TMaxC, &d.FetchedAt,
	)
	if err != nil {
		return WeatherDay{}, err
	}
	d.Date = dateFromTime(when)
	return d, nil
}
