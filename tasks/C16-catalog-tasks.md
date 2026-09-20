# C16 — Catalog: descriptions and task lists

**Depends on:** C15. **Blocks:** —.

**Owns:** `internal/catalog/`, `catalog/species/*.yaml`, `catalog/species/_template.yaml`.

## Goal

Move the eight committed species onto the task list, and say in YAML what each plant
is — so the care a plant needs is data the app can schedule rather than prose in
`care_advice` that a human has to read and act on.

## What to build

1. **YAML schema.** `description` (a paragraph on the species) alongside the existing
   short, practical `care_advice`. `prune:`, `fertilize:` and `repot:` are replaced by
   a `tasks:` list:

   ```yaml
   description: >-
     Tender annual herb grown for its leaves. Hates cold nights and wet feet;
     happiest in full sun with steady moisture and frequent picking.

   tasks:
     - slug: pinch-flowers
       kind: pinch
       label: Pinch flower spikes
       interval_days: 14
       active_months: [5, 6, 7, 8, 9]
     - slug: feed
       kind: fertilize
       label: Feed
       interval_days: 14
       active_months: [4, 5, 6, 7, 8, 9]
   ```

2. **Validation**, in the same spirit as the existing constant checks — the validator is
   the only thing standing between a typo and a plant that is never watered:
   - `slug` kebab-case, unique within the species, and never `water` (reserved, C15).
   - `kind` in the C15 vocabulary. An unknown kind is a hard error, not a warning.
   - `interval_days` in `[1, 3650]`; `active_months` values in `[1, 12]`, no duplicates.
   - `label` non-empty. It is what the digest line says, so "" would ship a blank
     reminder.
   - Strict decoding still rejects unknown fields, and the `base_interval_days`
     physics-consistency check is untouched — it concerns watering, which is unaffected.

3. **Rewrite the eight species.** Two are the reason this card exists:
   - **Basil** gets a real `pinch-flowers` task. Its `care_advice` currently explains
     the pinching in prose while the schedule calls it pruning.
   - **Lavender** splits into `prune-hard` (March, shape before growth) and
     `trim-after-flowering` (August). Its single 365-day prune is a compromise today.

   The rest map across mechanically; do not invent tasks for species that do not need
   them.

4. **`_template.yaml`** documents the vocabulary, the slug rule and the reserved
   `water` slug, and stops describing three fixed slots.

## Definition of done

- `go test ./internal/catalog/...` passes, including new cases for a duplicate slug, an
  unknown kind, a `water` slug and an empty label.
- All eight species validate, and lavender and basil schedule what their `care_advice`
  has been describing in prose.
- Adding a species is still one file and a pull request, with no Go code involved.
