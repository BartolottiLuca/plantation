package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueViolation is SQLSTATE 23505.
const uniqueViolation = "23505"

type SpeciesRepo struct {
	pool *pgxpool.Pool
}

func NewSpeciesRepo(pool *pgxpool.Pool) *SpeciesRepo {
	return &SpeciesRepo{pool: pool}
}

// Create inserts a new species and its tasks. A slug that already exists
// returns ErrConflict rather than overwriting: a slug is a permanent identity,
// and quietly replacing the species behind existing plants is never what a
// form submission means.
func (r *SpeciesRepo) Create(ctx context.Context, s domain.Species) error {
	return Retry(ctx, func(ctx context.Context) error {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("creating species %s: %w", s.Slug, err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		_, err = tx.Exec(ctx, `
			INSERT INTO species (
				slug, common_name, scientific_name, description, kc, substrate, mad,
				base_interval_days, min_interval_days, max_interval_days,
				dormant_months, dormancy_factor, min_temp_c, frost_tender,
				care_advice, retired, origin, ai_model, ai_drafted_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10,
				$11, $12, $13, $14,
				$15, $16, $17, $18, $19, now()
			)`,
			s.Slug, s.CommonName, s.ScientificName, s.Description,
			s.Kc, string(s.Substrate), s.MAD,
			s.BaseIntervalDays, s.MinIntervalDays, s.MaxIntervalDays,
			monthsToInts(s.DormantMonths), s.DormancyFactor, s.MinTempC, s.FrostTender,
			s.CareAdvice, s.Retired, originOrManual(s.Origin), nullIfEmpty(s.AIModel), s.AIDraftedAt,
		)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return fmt.Errorf("creating species %s: %w", s.Slug, ErrConflict)
			}
			return fmt.Errorf("creating species %s: %w", s.Slug, err)
		}
		if err := insertTasks(ctx, tx, s); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("creating species %s: %w", s.Slug, err)
		}
		return nil
	})
}

// Update overwrites an existing species and replaces its task list. Slug and
// provenance are not touched: the slug is identity, and an edit does not change
// who first wrote the record. Tasks are deleted and reinserted because nothing
// references species_tasks by key — care events and per-plant controls carry the
// task slug as plain text — so a task the form keeps under the same slug is
// indistinguishable from one that was never removed.
func (r *SpeciesRepo) Update(ctx context.Context, s domain.Species) error {
	return Retry(ctx, func(ctx context.Context) error {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("updating species %s: %w", s.Slug, err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		tag, err := tx.Exec(ctx, `
			UPDATE species SET
				common_name = $2, scientific_name = $3, description = $4,
				kc = $5, substrate = $6, mad = $7,
				base_interval_days = $8, min_interval_days = $9, max_interval_days = $10,
				dormant_months = $11, dormancy_factor = $12, min_temp_c = $13, frost_tender = $14,
				care_advice = $15, retired = $16, updated_at = now()
			WHERE slug = $1`,
			s.Slug, s.CommonName, s.ScientificName, s.Description,
			s.Kc, string(s.Substrate), s.MAD,
			s.BaseIntervalDays, s.MinIntervalDays, s.MaxIntervalDays,
			monthsToInts(s.DormantMonths), s.DormancyFactor, s.MinTempC, s.FrostTender,
			s.CareAdvice, s.Retired,
		)
		if err != nil {
			return fmt.Errorf("updating species %s: %w", s.Slug, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("updating species %s: %w", s.Slug, ErrNotFound)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM species_tasks WHERE species_slug = $1`, s.Slug); err != nil {
			return fmt.Errorf("clearing tasks for species %s: %w", s.Slug, err)
		}
		if err := insertTasks(ctx, tx, s); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("updating species %s: %w", s.Slug, err)
		}
		return nil
	})
}

func insertTasks(ctx context.Context, tx pgx.Tx, s domain.Species) error {
	for i, task := range s.Tasks {
		_, err := tx.Exec(ctx, `
			INSERT INTO species_tasks
				(species_slug, slug, kind, label, interval_days, active_months, sort_order, only_in)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			s.Slug, task.Slug, string(task.Kind), task.Label,
			task.IntervalDays, monthsToInts(task.ActiveMonths), i, nullIfEmpty(string(task.OnlyIn)))
		if err != nil {
			return fmt.Errorf("inserting task %s for species %s: %w", task.Slug, s.Slug, err)
		}
	}
	return nil
}

func (r *SpeciesRepo) List(ctx context.Context) ([]domain.Species, error) {
	var out []domain.Species
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, speciesSelect+" ORDER BY slug")
		if err != nil {
			return fmt.Errorf("listing species: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			s, err := scanSpecies(rows)
			if err != nil {
				return fmt.Errorf("listing species: %w", err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing species: %w", err)
		}
		return attachTasks(ctx, r.pool, out)
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.Species{}
	}
	return out, nil
}

func (r *SpeciesRepo) Get(ctx context.Context, slug string) (domain.Species, error) {
	var s domain.Species
	err := Retry(ctx, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, speciesSelect+" WHERE slug = $1", slug)
		got, err := scanSpecies(row)
		if err != nil {
			return fmt.Errorf("getting species %s: %w", slug, mapNoRows(err))
		}
		tasks, err := loadTasks(ctx, r.pool, []string{slug})
		if err != nil {
			return fmt.Errorf("getting species %s: %w", slug, err)
		}
		got.Tasks = tasks[slug]
		s = got
		return nil
	})
	return s, err
}

const speciesSelect = `
	SELECT slug, common_name, scientific_name, description, kc, substrate, mad,
		base_interval_days, min_interval_days, max_interval_days,
		dormant_months, dormancy_factor, min_temp_c, frost_tender,
		care_advice, retired, origin, ai_model, ai_drafted_at
	FROM species`

const speciesTasksSelect = `
	SELECT species_slug, slug, kind, label, interval_days, active_months, only_in
	FROM species_tasks`

type speciesScanner interface {
	Scan(dest ...any) error
}

func scanSpecies(row speciesScanner) (domain.Species, error) {
	var (
		s         domain.Species
		substrate string
		dormant   []int32
		origin    string
		aiModel   *string
	)
	err := row.Scan(
		&s.Slug, &s.CommonName, &s.ScientificName, &s.Description, &s.Kc, &substrate, &s.MAD,
		&s.BaseIntervalDays, &s.MinIntervalDays, &s.MaxIntervalDays,
		&dormant, &s.DormancyFactor, &s.MinTempC, &s.FrostTender,
		&s.CareAdvice, &s.Retired, &origin, &aiModel, &s.AIDraftedAt,
	)
	if err != nil {
		return domain.Species{}, err
	}
	s.Origin = domain.SpeciesOrigin(origin)
	if aiModel != nil {
		s.AIModel = *aiModel
	}
	s.Substrate = domain.SubstrateKind(substrate)
	s.DormantMonths = intsToMonths(dormant)
	return s, nil
}

// attachTasks batch-loads species_tasks for every species in the slice and
// sets each one's Tasks field. species_tasks is one-to-many, so it cannot be
// joined into the single-row species query without duplicating species rows.
func attachTasks(ctx context.Context, pool *pgxpool.Pool, species []domain.Species) error {
	slugs := make([]string, len(species))
	for i, s := range species {
		slugs[i] = s.Slug
	}
	tasks, err := loadTasks(ctx, pool, slugs)
	if err != nil {
		return err
	}
	for i := range species {
		species[i].Tasks = tasks[species[i].Slug]
	}
	return nil
}

func loadTasks(ctx context.Context, pool *pgxpool.Pool, slugs []string) (map[string][]domain.SpeciesTask, error) {
	out := map[string][]domain.SpeciesTask{}
	if len(slugs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, speciesTasksSelect+`
		WHERE species_slug = ANY($1)
		ORDER BY species_slug, sort_order`, slugs)
	if err != nil {
		return nil, fmt.Errorf("listing species tasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			speciesSlug string
			t           domain.SpeciesTask
			kind        string
			months      []int32
			onlyIn      *string
		)
		if err := rows.Scan(&speciesSlug, &t.Slug, &kind, &t.Label, &t.IntervalDays, &months, &onlyIn); err != nil {
			return nil, fmt.Errorf("listing species tasks: %w", err)
		}
		t.Kind = domain.TaskKind(kind)
		t.ActiveMonths = intsToMonths(months)
		if onlyIn != nil {
			t.OnlyIn = domain.Location(*onlyIn)
		}
		out[speciesSlug] = append(out[speciesSlug], t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing species tasks: %w", err)
	}
	return out, nil
}

func originOrManual(o domain.SpeciesOrigin) string {
	if o == "" {
		return string(domain.OriginManual)
	}
	return string(o)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
