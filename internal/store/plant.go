package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PlantRepo struct {
	pool *pgxpool.Pool
}

func NewPlantRepo(pool *pgxpool.Pool) *PlantRepo {
	return &PlantRepo{pool: pool}
}

func (r *PlantRepo) Create(ctx context.Context, p domain.Plant) (domain.Plant, error) {
	var out domain.Plant
	err := Retry(ctx, func(ctx context.Context) error {
		var id any
		if p.ID != uuid.Nil {
			id = p.ID
		}
		row := r.pool.QueryRow(ctx, `
			INSERT INTO plants (
				id, name, species_slug, location, place, tado_room_id, pot_diameter_mm,
				f_exposure, f_rain, acquired_at, active, notes,
				kc_override, mad_override, substrate_override,
				base_interval_days_override, min_interval_days_override, max_interval_days_override
			) VALUES (
				COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12,
				$13, $14, $15,
				$16, $17, $18
			)
			RETURNING `+plantColumns, append([]any{id}, plantArgs(p)...)...)
		got, err := scanPlant(row)
		if err != nil {
			return fmt.Errorf("creating plant: %w", err)
		}
		out = got
		return nil
	})
	return out, err
}

func (r *PlantRepo) Get(ctx context.Context, id uuid.UUID) (domain.Plant, error) {
	var p domain.Plant
	err := Retry(ctx, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `SELECT `+plantColumns+` FROM plants WHERE id = $1`, id)
		got, err := scanPlant(row)
		if err != nil {
			return fmt.Errorf("getting plant: %w", mapNoRows(err))
		}
		p = got
		return nil
	})
	return p, err
}

func (r *PlantRepo) Update(ctx context.Context, p domain.Plant) error {
	return Retry(ctx, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE plants SET
				name = $2, species_slug = $3, location = $4, place = $5, tado_room_id = $6,
				pot_diameter_mm = $7, f_exposure = $8, f_rain = $9, acquired_at = $10,
				active = $11, notes = $12, kc_override = $13, mad_override = $14,
				substrate_override = $15, base_interval_days_override = $16,
				min_interval_days_override = $17, max_interval_days_override = $18,
				updated_at = now()
			WHERE id = $1`,
			append([]any{p.ID}, plantArgs(p)...)...)
		if err != nil {
			return fmt.Errorf("updating plant: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r *PlantRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return Retry(ctx, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE plants SET active = false, updated_at = now() WHERE id = $1`, id)
		if err != nil {
			return fmt.Errorf("deleting plant: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r *PlantRepo) List(ctx context.Context) ([]PlantWithSpecies, error) {
	var out []PlantWithSpecies
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT `+plantColumnsPrefixed("p")+`,
				s.slug, s.common_name, s.scientific_name, s.placement, s.kc, s.substrate, s.mad,
				s.base_interval_days, s.min_interval_days, s.max_interval_days,
				s.dormant_months, s.dormancy_factor, s.min_temp_c, s.frost_tender,
				s.prune_interval_days, s.prune_months, s.fert_interval_days, s.fert_months,
				s.repot_interval_days, s.care_advice, s.retired
			FROM plants p
			JOIN species s ON s.slug = p.species_slug
			ORDER BY p.name, p.id`)
		if err != nil {
			return fmt.Errorf("listing plants: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			row, err := scanPlantWithSpecies(rows)
			if err != nil {
				return fmt.Errorf("listing plants: %w", err)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing plants: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []PlantWithSpecies{}
	}
	return out, nil
}

const plantColumns = `
	id, name, species_slug, location, place, tado_room_id, pot_diameter_mm,
	f_exposure, f_rain, acquired_at, active, notes,
	kc_override, mad_override, substrate_override,
	base_interval_days_override, min_interval_days_override, max_interval_days_override`

func plantColumnsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.name, ` + alias + `.species_slug, ` + alias + `.location, ` +
		alias + `.place, ` + alias + `.tado_room_id, ` + alias + `.pot_diameter_mm, ` +
		alias + `.f_exposure, ` + alias + `.f_rain, ` + alias + `.acquired_at, ` +
		alias + `.active, ` + alias + `.notes, ` +
		alias + `.kc_override, ` + alias + `.mad_override, ` + alias + `.substrate_override, ` +
		alias + `.base_interval_days_override, ` + alias + `.min_interval_days_override, ` +
		alias + `.max_interval_days_override`
}

func plantArgs(p domain.Plant) []any {
	var substrate any
	if p.Overrides.Substrate != nil {
		substrate = string(*p.Overrides.Substrate)
	}
	return []any{
		p.Name, p.SpeciesSlug, string(p.Location), p.Place, p.TadoRoomID, potArg(p),
		p.FExposure, p.FRain, p.AcquiredAt, p.Active, p.Notes,
		p.Overrides.Kc, p.Overrides.MAD, substrate,
		p.Overrides.BaseIntervalDays, p.Overrides.MinIntervalDays, p.Overrides.MaxIntervalDays,
	}
}

func potArg(p domain.Plant) any {
	if p.InGround {
		return nil
	}
	return p.PotDiameterMM
}

func scanPlant(row speciesScanner) (domain.Plant, error) {
	var (
		p         domain.Plant
		location  string
		substrate *string
		pot       sql.NullInt32
	)
	err := row.Scan(
		&p.ID, &p.Name, &p.SpeciesSlug, &location, &p.Place, &p.TadoRoomID, &pot,
		&p.FExposure, &p.FRain, &p.AcquiredAt, &p.Active, &p.Notes,
		&p.Overrides.Kc, &p.Overrides.MAD, &substrate,
		&p.Overrides.BaseIntervalDays, &p.Overrides.MinIntervalDays, &p.Overrides.MaxIntervalDays,
	)
	if err != nil {
		return domain.Plant{}, err
	}
	p.Location = domain.Location(location)
	applyPot(&p, pot)
	if substrate != nil {
		s := domain.SubstrateKind(*substrate)
		p.Overrides.Substrate = &s
	}
	return p, nil
}

func applyPot(p *domain.Plant, pot sql.NullInt32) {
	if !pot.Valid {
		p.InGround = true
		return
	}
	p.PotDiameterMM = int(pot.Int32)
}

func scanPlantWithSpecies(row speciesScanner) (PlantWithSpecies, error) {
	var (
		p           domain.Plant
		location    string
		substrate   *string
		pot         sql.NullInt32
		s           domain.Species
		sPlacement  string
		sSubstrate  string
		dormant     []int32
		pruneDays   *int
		pruneMonths []int32
		fertDays    *int
		fertMonths  []int32
		repotDays   *int
	)
	err := row.Scan(
		&p.ID, &p.Name, &p.SpeciesSlug, &location, &p.Place, &p.TadoRoomID, &pot,
		&p.FExposure, &p.FRain, &p.AcquiredAt, &p.Active, &p.Notes,
		&p.Overrides.Kc, &p.Overrides.MAD, &substrate,
		&p.Overrides.BaseIntervalDays, &p.Overrides.MinIntervalDays, &p.Overrides.MaxIntervalDays,
		&s.Slug, &s.CommonName, &s.ScientificName, &sPlacement, &s.Kc, &sSubstrate, &s.MAD,
		&s.BaseIntervalDays, &s.MinIntervalDays, &s.MaxIntervalDays,
		&dormant, &s.DormancyFactor, &s.MinTempC, &s.FrostTender,
		&pruneDays, &pruneMonths, &fertDays, &fertMonths,
		&repotDays, &s.CareAdvice, &s.Retired,
	)
	if err != nil {
		return PlantWithSpecies{}, err
	}
	p.Location = domain.Location(location)
	applyPot(&p, pot)
	if substrate != nil {
		sk := domain.SubstrateKind(*substrate)
		p.Overrides.Substrate = &sk
	}
	s.Placement = domain.Location(sPlacement)
	s.Substrate = domain.SubstrateKind(sSubstrate)
	s.DormantMonths = intsToMonths(dormant)
	s.Prune = optionalFixed(pruneDays, pruneMonths)
	s.Fertilize = optionalFixed(fertDays, fertMonths)
	if repotDays != nil {
		s.Repot = &domain.FixedTask{IntervalDays: *repotDays}
	}
	return PlantWithSpecies{Plant: p, Species: s}, nil
}
