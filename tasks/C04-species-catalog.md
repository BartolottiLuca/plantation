# C04 — Species catalog

**Depends on:** C01, C02. **Blocks:** C10 (species picker).

**Owns:** `catalog/species/**`, `internal/catalog/**`.

## Goal

Curated species data as YAML in the repo, embedded in the binary, validated hard, and
upserted into Postgres at boot. Adding a species must be one file and a pull request,
with no Go code and no migration — that is the workflow an agent will use, so the
failure messages matter as much as the parser.

## What to build

1. **Schema.** One file per species, `catalog/species/<slug>.yaml`, mirroring
   `domain.Species` ([SPEC.md](../SPEC.md) §3). Slug is the filename and the scientific
   name in kebab-case. Write `catalog/species/_template.yaml` with every field, its
   units, its allowed range, and a one-line comment on how to choose — that template is
   the real documentation.

2. **Loader.** `go:embed catalog/species/*.yaml`, parsed with
   `yaml.Decoder.KnownFields(true)` so a typo is an error rather than a silent zero.

3. **Validator.** Range-check every constant against SPEC §7.2 (`Kc` 0.1–1.5, `MAD`
   0.2–0.9, substrate in the enum, `min < base < max` intervals, months 1–12,
   `dormancy_factor` 0.2–1.0). Plus the **physics-consistency check**: recompute the
   interval the model produces at ET0 = 3 mm/day in an 18 cm pot and fail when the
   declared `base_interval_days` is more than 40 % away from it. That check is the point
   of keeping both numbers — it catches an entry where somebody picked a plausible
   interval and an implausible `Kc`.

   Every failure message names the file, the slug and the field:
   `catalog/species/ficus-lyrata.yaml: ficus-lyrata: kc 2.4 out of range [0.1, 1.5]`.

4. **Boot upsert.** Called from `cmd` after migrations: wholesale overwrite of the
   `species` table from YAML. The table is a read-through projection — the app never
   writes it at runtime. A species that disappears from the catalog but still has plants
   referencing it is marked `retired = true`, never deleted.

5. **Seed data.** The `_template.yaml`, plus these eight to prove the shape across the
   range of archetypes: `monstera-deliciosa`, `ficus-lyrata`, `sansevieria-trifasciata`,
   `echeveria-elegans`, `nephrolepis-exaltata` (fern), `ocimum-basilicum` (basil),
   `lavandula-angustifolia`, `hydrangea-macrophylla`. Pick constants by archetype from
   SPEC §7.2 rather than inventing numbers, and put the real horticultural advice in
   `care_advice` — it is what the UI shows on the plant page.

   Luca's actual plants get added later, by the same one-file workflow.

## Definition of done

- Adding a species requires editing exactly one file; no Go change, no migration.
- Validation rejects an unknown field, an out-of-range constant, a missing required
  field and an inconsistent `base_interval_days`, each with a message naming file, slug
  and field. There is a test per failure mode using a fixture in `testdata/`.
- The eight seeded species pass validation, and a test asserts they do — so a bad edit
  to the catalog fails CI.
- Upsert run twice is a no-op; a species removed from the catalog while plants reference
  it ends up `retired = true` rather than deleted or erroring.
- The `AGENTS.md` "Adding a species" recipe matches what the code actually does.
