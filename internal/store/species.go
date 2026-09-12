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
				slug, common_name, scientific_name, placement, kc, substrate, mad,
				base_interval_days, min_interval_days, max_interval_days,
				dormant_months, dormancy_factor, min_temp_c, frost_tender,
				prune_interval_days, prune_months, fert_interval_days, fert_months,
				repot_interval_days, care_advice, retired, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10,
				$11, $12, $13, $14,
				$15, $16, $17, $18,
				$19, $20, $21, now()
			)
			ON CONFLICT (slug) DO UPDATE SET
				common_name = EXCLUDED.common_name,
				scientific_name = EXCLUDED.scientific_name,
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
				prune_interval_days = EXCLUDED.prune_interval_days,
				prune_months = EXCLUDED.prune_months,
				fert_interval_days = EXCLUDED.fert_interval_days,
				fert_months = EXCLUDED.fert_months,
				repot_interval_days = EXCLUDED.repot_interval_days,
				care_advice = EXCLUDED.care_advice,
				retired = EXCLUDED.retired,
				updated_at = now()`

		for _, s := range species {
			pruneDays, pruneMonths := fixedTaskArgs(s.Prune)
			fertDays, fertMonths := fixedTaskArgs(s.Fertilize)
			var repotDays any
			if s.Repot != nil {
				repotDays = s.Repot.IntervalDays
			}
			_, err := tx.Exec(ctx, q,
				s.Slug, s.CommonName, s.ScientificName, string(s.Placement),
				s.Kc, string(s.Substrate), s.MAD,
				s.BaseIntervalDays, s.MinIntervalDays, s.MaxIntervalDays,
				monthsToInts(s.DormantMonths), s.DormancyFactor, s.MinTempC, s.FrostTender,
				pruneDays, pruneMonths, fertDays, fertMonths,
				repotDays, s.CareAdvice, s.Retired,
			)
			if err != nil {
				return fmt.Errorf("upserting species %s: %w", s.Slug, err)
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
		return nil
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
		s = got
		return nil
	})
	return s, err
}

const speciesSelect = `
	SELECT slug, common_name, scientific_name, placement, kc, substrate, mad,
		base_interval_days, min_interval_days, max_interval_days,
		dormant_months, dormancy_factor, min_temp_c, frost_tender,
		prune_interval_days, prune_months, fert_interval_days, fert_months,
		repot_interval_days, care_advice, retired
	FROM species`

type speciesScanner interface {
	Scan(dest ...any) error
}

func scanSpecies(row speciesScanner) (domain.Species, error) {
	var (
		s           domain.Species
		placement   string
		substrate   string
		dormant     []int32
		pruneDays   *int
		pruneMonths []int32
		fertDays    *int
		fertMonths  []int32
		repotDays   *int
	)
	err := row.Scan(
		&s.Slug, &s.CommonName, &s.ScientificName, &placement, &s.Kc, &substrate, &s.MAD,
		&s.BaseIntervalDays, &s.MinIntervalDays, &s.MaxIntervalDays,
		&dormant, &s.DormancyFactor, &s.MinTempC, &s.FrostTender,
		&pruneDays, &pruneMonths, &fertDays, &fertMonths,
		&repotDays, &s.CareAdvice, &s.Retired,
	)
	if err != nil {
		return domain.Species{}, err
	}
	s.Placement = domain.Location(placement)
	s.Substrate = domain.SubstrateKind(substrate)
	s.DormantMonths = intsToMonths(dormant)
	s.Prune = optionalFixed(pruneDays, pruneMonths)
	s.Fertilize = optionalFixed(fertDays, fertMonths)
	if repotDays != nil {
		s.Repot = &domain.FixedTask{IntervalDays: *repotDays}
	}
	return s, nil
}

func fixedTaskArgs(t *domain.FixedTask) (days any, months []int32) {
	if t == nil {
		return nil, []int32{}
	}
	return t.IntervalDays, monthsToInts(t.ActiveMonths)
}

func optionalFixed(days *int, months []int32) *domain.FixedTask {
	if days == nil {
		return nil
	}
	return &domain.FixedTask{
		IntervalDays: *days,
		ActiveMonths: intsToMonths(months),
	}
}
