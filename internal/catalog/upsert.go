package catalog

import (
	"context"
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// UpsertLister is the slice of store.SpeciesRepo that Upsert needs.
// store.SpeciesRepo satisfies it structurally; this package never imports
// internal/store.
type UpsertLister interface {
	UpsertAll(ctx context.Context, species []domain.Species) error
	List(ctx context.Context) ([]domain.Species, error)
}

// Upsert wholesale-overwrites the species table from the loaded catalog,
// except it never deletes: a species that existed in the database but is
// absent from loaded (because the YAML file was removed) is kept, with
// Retired forced true, so a plant still referencing it doesn't dangle. It is
// idempotent — running it twice against the same loaded set and the same
// starting database state produces the same UpsertAll argument both times.
func Upsert(ctx context.Context, repo UpsertLister, loaded []domain.Species) error {
	existing, err := repo.List(ctx)
	if err != nil {
		return fmt.Errorf("listing existing species: %w", err)
	}

	present := make(map[string]bool, len(loaded))
	merged := make([]domain.Species, 0, len(loaded)+len(existing))
	merged = append(merged, loaded...)
	for _, s := range loaded {
		present[s.Slug] = true
	}
	for _, old := range existing {
		if present[old.Slug] {
			continue
		}
		old.Retired = true
		merged = append(merged, old)
	}

	if err := repo.UpsertAll(ctx, merged); err != nil {
		return fmt.Errorf("upserting species catalog: %w", err)
	}
	return nil
}
