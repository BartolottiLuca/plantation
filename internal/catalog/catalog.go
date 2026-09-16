package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	speciesfs "github.com/BartolottiLuca/plantation/catalog/species"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

// catalogDirLabel prefixes every validation error so it names a path an agent
// can open directly, matching the real location of the embedded YAML.
const catalogDirLabel = "catalog/species"

// Load embeds, parses and validates the whole species catalog once. Every
// validation failure across every file is aggregated into the single
// returned error (via errors.Join) rather than stopping at the first —
// running it is the last step of "adding a species" in AGENTS.md, so a
// contributor should see every problem in one pass.
func Load() ([]domain.Species, error) {
	return loadFS(speciesfs.FS, catalogDirLabel)
}

// loadFS is the direction-agnostic core of Load, exercised directly by tests
// against fixtures under testdata so a validation failure mode can be pinned
// down without touching the real embedded catalog.
func loadFS(fsys fs.FS, dirLabel string) ([]domain.Species, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dirLabel, err)
	}

	var out []domain.Species
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if strings.HasPrefix(e.Name(), "_") {
			continue // _template.yaml and any future documentation-only file
		}
		slug := strings.TrimSuffix(e.Name(), ".yaml")
		file := dirLabel + "/" + e.Name()

		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: reading: %w", file, slug, err))
			continue
		}
		y, err := decode(file, slug, data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if fieldErrs := validate(file, slug, y); len(fieldErrs) > 0 {
			errs = append(errs, fieldErrs...)
			continue
		}
		out = append(out, convert(slug, y))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
