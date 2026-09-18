package store

import (
	"context"
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionRepo deletes telemetry older than a cutoff. Care events are not
// in scope: the watering model still needs the last water, which can be
// months ago.
type RetentionRepo struct {
	pool *pgxpool.Pool
}

func NewRetentionRepo(pool *pgxpool.Pool) *RetentionRepo {
	return &RetentionRepo{pool: pool}
}

func (r *RetentionRepo) PurgeOlderThan(ctx context.Context, before time.Time) error {
	return Retry(ctx, func(ctx context.Context) error {
		y, m, d := before.Date()
		weatherBefore := civilDate(domain.Date{Year: y, Month: m, Day: d})
		if _, err := r.pool.Exec(ctx, `DELETE FROM weather_daily WHERE date < $1`, weatherBefore); err != nil {
			return fmt.Errorf("purging weather: %w", err)
		}
		if _, err := r.pool.Exec(ctx, `DELETE FROM room_climate_samples WHERE observed_at < $1`, before); err != nil {
			return fmt.Errorf("purging climate samples: %w", err)
		}
		if _, err := r.pool.Exec(ctx, `DELETE FROM notifications WHERE claimed_at < $1`, before); err != nil {
			return fmt.Errorf("purging notifications: %w", err)
		}
		return nil
	})
}
