# C17 — Store: species tasks, event identity, re-keyed controls

**Depends on:** C15. **Blocks:** C18.

**Owns:** `internal/store/migrations/0003_species_tasks.sql`, `internal/store/species.go`,
`internal/store/care_event.go`, `internal/store/care_task.go`, `internal/store/types.go`.

## Goal

Persist the task list, and give care events the identity they need to say *which* task
was done — not merely what kind of thing it was.

**This is the riskiest card in the phase.** `care_events` is append-only and, per SPEC
§5, the only irreplaceable data in the database: weather, climate samples and
notifications are caches, and `species` is rebuilt from YAML at every boot. A migration
that loses or mangles care events cannot be recovered from the catalog.

## What to build

1. **`species_tasks`**, rebuilt wholesale at boot exactly as `species` is:

   ```sql
   CREATE TABLE species_tasks (
     species_slug  text NOT NULL REFERENCES species(slug) ON DELETE CASCADE,
     slug          text NOT NULL,
     kind          text NOT NULL,
     label         text NOT NULL,
     interval_days int  NOT NULL,
     active_months int[] NOT NULL DEFAULT '{}',
     sort_order    int  NOT NULL DEFAULT 0,
     PRIMARY KEY (species_slug, slug)
   );
   ```

   `species` drops `prune_interval_days`, `prune_months`, `fert_interval_days`,
   `fert_months` and `repot_interval_days`, and gains `description text NOT NULL
   DEFAULT ''`.

2. **`care_events` gains `task_slug text`, nullable.** Nullable is the whole design:
   every existing row keeps its `kind` and gets no slug, and C18 treats a slug-less
   event as satisfying any task of that kind. Backfilling a slug would mean guessing,
   and for a species that now has two tasks of one kind the guess is undecidable.

3. **`care_tasks` re-keys from `(plant_id, kind)` to `(plant_id, task_slug)`.** Migrate
   existing control rows by mapping each `kind` to that species' task of the same kind.
   Today that mapping is exact — every species has at most one task per kind — so the
   migration does not guess. Write it so it fails loudly rather than dropping a row if
   it ever finds two candidates.

4. **Repositories** updated: species upsert writes `species_tasks`, events read and write
   `task_slug`, controls key on slug. Keep the batch reads from the last round
   (`LatestByKindForPlants`, `ListForPlants`) — the dashboard depends on them being one
   query each.

## Definition of done

- Migrations run twice against a fresh database with identical end state (SPEC §15).
- A test migrates a database **seeded with pre-migration data** — care events of each
  old kind, and control rows — and asserts every event survives with its `kind` intact
  and `task_slug` NULL, and that each control row lands on the right slug.
- `go test ./internal/store/... -race` passes with `PLANTATION_TEST_DSN` set.
- Run the migration against a copy of the live database before this is deployed. The
  test proves the shape; only real rows prove the data.
