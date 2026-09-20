package store

import (
	"context"
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CareEventRepo struct {
	pool *pgxpool.Pool
}

func NewCareEventRepo(pool *pgxpool.Pool) *CareEventRepo {
	return &CareEventRepo{pool: pool}
}

func (r *CareEventRepo) Add(ctx context.Context, e domain.CareEvent) (domain.CareEvent, error) {
	var out domain.CareEvent
	err := Retry(ctx, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `
			INSERT INTO care_events (plant_id, kind, task_slug, done_at, note, source)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, plant_id, kind, task_slug, done_at, note, source, voided_at`,
			e.PlantID, string(e.Kind), e.TaskSlug, e.DoneAt, e.Note, e.Source)
		got, err := scanCareEvent(row)
		if err != nil {
			return fmt.Errorf("adding care event: %w", err)
		}
		out = got
		return nil
	})
	return out, err
}

func (r *CareEventRepo) Void(ctx context.Context, id int64) error {
	return Retry(ctx, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE care_events SET voided_at = now()
			WHERE id = $1 AND voided_at IS NULL`, id)
		if err != nil {
			return fmt.Errorf("voiding care event: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// LatestByKind returns the newest non-voided event for each distinct (kind,
// task_slug) pair — not each kind alone. A NULL task_slug is its own group per
// kind (the legacy bucket a pre-migration event falls into); a real slug is
// its own group regardless of kind, since a slug belongs to exactly one
// species task. Two tasks sharing a kind — lavender's two prunings — therefore
// keep independent "last done" dates instead of one collapsing onto the other.
func (r *CareEventRepo) LatestByKind(ctx context.Context, plantID uuid.UUID) ([]domain.CareEvent, error) {
	var out []domain.CareEvent
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT DISTINCT ON (kind, task_slug)
				id, plant_id, kind, task_slug, done_at, note, source, voided_at
			FROM care_events
			WHERE plant_id = $1 AND voided_at IS NULL
			ORDER BY kind, task_slug, done_at DESC, id DESC`, plantID)
		if err != nil {
			return fmt.Errorf("latest care events: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			e, err := scanCareEvent(rows)
			if err != nil {
				return fmt.Errorf("latest care events: %w", err)
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("latest care events: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.CareEvent{}
	}
	return out, nil
}

func (r *CareEventRepo) SinceDate(ctx context.Context, plantID uuid.UUID, kind domain.TaskKind, from domain.Date) ([]domain.CareEvent, error) {
	var out []domain.CareEvent
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT id, plant_id, kind, task_slug, done_at, note, source, voided_at
			FROM care_events
			WHERE plant_id = $1 AND kind = $2 AND done_at >= $3
			ORDER BY done_at ASC, id ASC`,
			plantID, string(kind), civilDate(from))
		if err != nil {
			return fmt.Errorf("listing care events: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			e, err := scanCareEvent(rows)
			if err != nil {
				return fmt.Errorf("listing care events: %w", err)
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing care events: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.CareEvent{}
	}
	return out, nil
}

func scanCareEvent(row speciesScanner) (domain.CareEvent, error) {
	var (
		e        domain.CareEvent
		kind     string
		taskSlug *string
		voided   *time.Time
	)
	if err := row.Scan(&e.ID, &e.PlantID, &kind, &taskSlug, &e.DoneAt, &e.Note, &e.Source, &voided); err != nil {
		return domain.CareEvent{}, err
	}
	e.Kind = domain.TaskKind(kind)
	e.TaskSlug = taskSlug
	e.VoidedAt = voided
	return e, nil
}

// LatestByKindForPlants is the batch form of LatestByKind: the newest
// non-voided event per (plant, kind, task_slug) for every plant named.
func (r *CareEventRepo) LatestByKindForPlants(ctx context.Context, plantIDs []uuid.UUID) (map[uuid.UUID][]domain.CareEvent, error) {
	out := map[uuid.UUID][]domain.CareEvent{}
	if len(plantIDs) == 0 {
		return out, nil
	}
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT DISTINCT ON (plant_id, kind, task_slug)
				id, plant_id, kind, task_slug, done_at, note, source, voided_at
			FROM care_events
			WHERE plant_id = ANY($1) AND voided_at IS NULL
			ORDER BY plant_id, kind, task_slug, done_at DESC, id DESC`, plantIDs)
		if err != nil {
			return fmt.Errorf("latest care events: %w", err)
		}
		defer rows.Close()
		clear(out)
		for rows.Next() {
			e, err := scanCareEvent(rows)
			if err != nil {
				return fmt.Errorf("latest care events: %w", err)
			}
			out[e.PlantID] = append(out[e.PlantID], e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("latest care events: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
