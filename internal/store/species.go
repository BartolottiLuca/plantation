package store

import (
	"context"
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SpeciesRepo struct {
	pool *pgxpool.Pool
}

func NewSpeciesRepo(pool *pgxpool.Pool) *SpeciesRepo {
	return &SpeciesRepo{pool: pool}
}

func (r *SpeciesRepo) UpsertAll(ctx context.Context, species []domain.Species) error {
	return Retry(ctx, func(ctx context.Context) error {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("upserting species: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		const q = `
			INSERT INTO species (
				slug, common_name, scientific_name, description, placement, kc, substrate, mad,
				base_interval_days, min_interval_days, max_interval_days,
				dormant_months, dormancy_factor, min_temp_c, frost_tender,
				care_advice, retired, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				$9, $10, $11,
				$12, $13, $14, $15,
				$16, $17, now()
			)
			ON CONFLICT (slug) DO UPDATE SET
				common_name = EXCLUDED.common_name,
				scientific_name = EXCLUDED.scientific_name,
				description = EXCLUDED.description,
				placement = EXCLUDED.placement,
				kc = EXCLUDED.kc,
				substrate = EXCLUDED.substrate,
				mad = EXCLUDED.mad,
				base_interval_days = EXCLUDED.base_interval_days,
				min_interval_days = EXCLUDED.min_interval_days,
				max_interval_days = EXCLUDED.max_interval_days,
				dormant_months = EXCLUDED.dormant_months,
				dormancy_factor = EXCLUDED.dormancy_factor,
				min_temp_c = EXCLUDED.min_temp_c,
				frost_tender = EXCLUDED.frost_tender,
				care_advice = EXCLUDED.care_advice,
				retired = EXCLUDED.retired,
				updated_at = now()`

		for _, s := range species {
			_, err := tx.Exec(ctx, q,
				s.Slug, s.CommonName, s.ScientificName, s.Description, string(s.Placement),
				s.Kc, string(s.Substrate), s.MAD,
				s.BaseIntervalDays, s.MinIntervalDays, s.MaxIntervalDays,
				monthsToInts(s.DormantMonths), s.DormancyFactor, s.MinTempC, s.FrostTender,
				s.CareAdvice, s.Retired,
			)
			if err != nil {
				return fmt.Errorf("upserting species %s: %w", s.Slug, err)
			}
			// species_tasks is rebuilt wholesale per species, exactly like species
			// itself: delete-then-insert is simpler and just as correct as diffing
			// when the whole set is rewritten at every boot anyway.
			if _, err := tx.Exec(ctx, `DELETE FROM species_tasks WHERE species_slug = $1`, s.Slug); err != nil {
				return fmt.Errorf("clearing tasks for species %s: %w", s.Slug, err)
			}
			for i, task := range s.Tasks {
				_, err := tx.Exec(ctx, `
					INSERT INTO species_tasks
						(species_slug, slug, kind, label, interval_days, active_months, sort_order)
					VALUES ($1, $2, $3, $4, $5, $6, $7)`,
					s.Slug, task.Slug, string(task.Kind), task.Label,
					task.IntervalDays, monthsToInts(task.ActiveMonths), i)
				if err != nil {
					return fmt.Errorf("upserting task %s for species %s: %w", task.Slug, s.Slug, err)
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("upserting species: %w", err)
		}
		return nil
	})
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
	SELECT slug, common_name, scientific_name, description, placement, kc, substrate, mad,
		base_interval_days, min_interval_days, max_interval_days,
		dormant_months, dormancy_factor, min_temp_c, frost_tender,
		care_advice, retired
	FROM species`

const speciesTasksSelect = `
	SELECT species_slug, slug, kind, label, interval_days, active_months
	FROM species_tasks`

type speciesScanner interface {
	Scan(dest ...any) error
}

func scanSpecies(row speciesScanner) (domain.Species, error) {
	var (
		s         domain.Species
		placement string
		substrate string
		dormant   []int32
	)
	err := row.Scan(
		&s.Slug, &s.CommonName, &s.ScientificName, &s.Description, &placement, &s.Kc, &substrate, &s.MAD,
		&s.BaseIntervalDays, &s.MinIntervalDays, &s.MaxIntervalDays,
		&dormant, &s.DormancyFactor, &s.MinTempC, &s.FrostTender,
		&s.CareAdvice, &s.Retired,
	)
	if err != nil {
		return domain.Species{}, err
	}
	s.Placement = domain.Location(placement)
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
		)
		if err := rows.Scan(&speciesSlug, &t.Slug, &kind, &t.Label, &t.IntervalDays, &months); err != nil {
			return nil, fmt.Errorf("listing species tasks: %w", err)
		}
		t.Kind = domain.TaskKind(kind)
		t.ActiveMonths = intsToMonths(months)
		out[speciesSlug] = append(out[speciesSlug], t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing species tasks: %w", err)
	}
	return out, nil
}
