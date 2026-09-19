package store

import (
	"context"
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CareTaskRepo struct {
	pool *pgxpool.Pool
}

func NewCareTaskRepo(pool *pgxpool.Pool) *CareTaskRepo {
	return &CareTaskRepo{pool: pool}
}

func (r *CareTaskRepo) List(ctx context.Context, plantID uuid.UUID) ([]CareTask, error) {
	var out []CareTask
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT id, plant_id, kind, enabled, interval_days_override, snoozed_until
			FROM care_tasks
			WHERE plant_id = $1
			ORDER BY kind`, plantID)
		if err != nil {
			return fmt.Errorf("listing care tasks: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			t, err := scanCareTask(rows)
			if err != nil {
				return fmt.Errorf("listing care tasks: %w", err)
			}
			out = append(out, t)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing care tasks: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []CareTask{}
	}
	return out, nil
}

func (r *CareTaskRepo) Upsert(ctx context.Context, t CareTask) (CareTask, error) {
	var out CareTask
	err := Retry(ctx, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `
			INSERT INTO care_tasks (plant_id, kind, enabled, interval_days_override, snoozed_until)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (plant_id, kind) DO UPDATE SET
				enabled = EXCLUDED.enabled,
				interval_days_override = EXCLUDED.interval_days_override,
				snoozed_until = EXCLUDED.snoozed_until
			RETURNING id, plant_id, kind, enabled, interval_days_override, snoozed_until`,
			t.PlantID, string(t.Kind), t.Enabled, t.IntervalDaysOverride, dateArg(t.SnoozedUntil))
		got, err := scanCareTask(row)
		if err != nil {
			return fmt.Errorf("upserting care task: %w", err)
		}
		out = got
		return nil
	})
	return out, err
}

func (r *CareTaskRepo) Enable(ctx context.Context, plantID uuid.UUID, kind domain.TaskKind) error {
	return r.setEnabled(ctx, plantID, kind, true)
}

func (r *CareTaskRepo) Disable(ctx context.Context, plantID uuid.UUID, kind domain.TaskKind) error {
	return r.setEnabled(ctx, plantID, kind, false)
}

func (r *CareTaskRepo) setEnabled(ctx context.Context, plantID uuid.UUID, kind domain.TaskKind, enabled bool) error {
	return Retry(ctx, func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `
			INSERT INTO care_tasks (plant_id, kind, enabled)
			VALUES ($1, $2, $3)
			ON CONFLICT (plant_id, kind) DO UPDATE SET enabled = EXCLUDED.enabled`,
			plantID, string(kind), enabled)
		if err != nil {
			return fmt.Errorf("setting care task enabled: %w", err)
		}
		return nil
	})
}

func (r *CareTaskRepo) Snooze(ctx context.Context, plantID uuid.UUID, kind domain.TaskKind, until *domain.Date) error {
	return Retry(ctx, func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `
			INSERT INTO care_tasks (plant_id, kind, snoozed_until)
			VALUES ($1, $2, $3)
			ON CONFLICT (plant_id, kind) DO UPDATE SET snoozed_until = EXCLUDED.snoozed_until`,
			plantID, string(kind), dateArg(until))
		if err != nil {
			return fmt.Errorf("snoozing care task: %w", err)
		}
		return nil
	})
}

func scanCareTask(row speciesScanner) (CareTask, error) {
	var (
		t     CareTask
		kind  string
		until *time.Time
	)
	if err := row.Scan(&t.ID, &t.PlantID, &kind, &t.Enabled, &t.IntervalDaysOverride, &until); err != nil {
		return CareTask{}, err
	}
	t.Kind = domain.TaskKind(kind)
	t.SnoozedUntil = optionalDate(until)
	return t, nil
}

// ListForPlants is the batch form of List. The dashboard schedules every plant
// on one render, so a per-plant query there is one round trip per plant.
func (r *CareTaskRepo) ListForPlants(ctx context.Context, plantIDs []uuid.UUID) (map[uuid.UUID][]CareTask, error) {
	out := map[uuid.UUID][]CareTask{}
	if len(plantIDs) == 0 {
		return out, nil
	}
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT id, plant_id, kind, enabled, interval_days_override, snoozed_until
			FROM care_tasks
			WHERE plant_id = ANY($1)
			ORDER BY plant_id, kind`, plantIDs)
		if err != nil {
			return fmt.Errorf("listing care tasks: %w", err)
		}
		defer rows.Close()
		clear(out)
		for rows.Next() {
			t, err := scanCareTask(rows)
			if err != nil {
				return fmt.Errorf("listing care tasks: %w", err)
			}
			out[t.PlantID] = append(out[t.PlantID], t)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing care tasks: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
