package store

import (
	"context"
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ClimateRepo struct {
	pool *pgxpool.Pool
}

func NewClimateRepo(pool *pgxpool.Pool) *ClimateRepo {
	return &ClimateRepo{pool: pool}
}

func (r *ClimateRepo) AddSamples(ctx context.Context, samples []ClimateSample) error {
	return Retry(ctx, func(ctx context.Context) error {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("adding climate samples: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		const q = `
			INSERT INTO room_climate_samples (tado_room_id, observed_at, temp_c, humidity_pct)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tado_room_id, observed_at) DO NOTHING`

		for _, s := range samples {
			if _, err := tx.Exec(ctx, q, s.RoomID, s.ObservedAt, s.TempC, s.HumidityPct); err != nil {
				return fmt.Errorf("adding climate samples: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("adding climate samples: %w", err)
		}
		return nil
	})
}

// DailyMeans groups samples by UTC civil date. The household timezone is
// applied by the caller when converting domain.Date bounds.
func (r *ClimateRepo) DailyMeans(ctx context.Context, roomID string, from, to domain.Date) ([]ClimateDailyMean, error) {
	var out []ClimateDailyMean
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT (observed_at AT TIME ZONE 'UTC')::date AS day,
				avg(temp_c), avg(humidity_pct)
			FROM room_climate_samples
			WHERE tado_room_id = $1
				AND observed_at >= $2
				AND observed_at < $3
			GROUP BY day
			ORDER BY day`,
			roomID, civilDate(from), civilDate(to).AddDate(0, 0, 1))
		if err != nil {
			return fmt.Errorf("climate daily means: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			var (
				m    ClimateDailyMean
				when time.Time
			)
			if err := rows.Scan(&when, &m.TempC, &m.HumidityPct); err != nil {
				return fmt.Errorf("climate daily means: %w", err)
			}
			m.Date = dateFromTime(when)
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("climate daily means: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []ClimateDailyMean{}
	}
	return out, nil
}

func (r *ClimateRepo) LatestSample(ctx context.Context, roomID string) (ClimateSample, error) {
	var s ClimateSample
	err := Retry(ctx, func(ctx context.Context) error {
		err := r.pool.QueryRow(ctx, `
			SELECT tado_room_id, observed_at, temp_c, humidity_pct
			FROM room_climate_samples
			WHERE tado_room_id = $1
			ORDER BY observed_at DESC
			LIMIT 1`, roomID).Scan(&s.RoomID, &s.ObservedAt, &s.TempC, &s.HumidityPct)
		if err != nil {
			return fmt.Errorf("latest climate sample: %w", mapNoRows(err))
		}
		return nil
	})
	return s, err
}
