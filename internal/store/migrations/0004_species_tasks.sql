-- Species care moves from three fixed columns to a list, so a species can
-- declare any number of tasks — including two of the same kind, which the
-- three-column shape could never express (lavender wants a hard prune in
-- spring and a light trim after flowering).

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

-- One row per existing fixed task, in the same display order the three
-- columns implied (prune, then fertilize, then repot). Slugs match the kind:
-- every species has at most one task per kind today, so this is exact, not a
-- guess.
INSERT INTO species_tasks (species_slug, slug, kind, label, interval_days, active_months, sort_order)
SELECT slug, 'prune', 'prune', 'Prune', prune_interval_days, prune_months, 0
FROM species WHERE prune_interval_days IS NOT NULL;

INSERT INTO species_tasks (species_slug, slug, kind, label, interval_days, active_months, sort_order)
SELECT slug, 'fertilize', 'fertilize', 'Fertilize', fert_interval_days, fert_months, 1
FROM species WHERE fert_interval_days IS NOT NULL;

INSERT INTO species_tasks (species_slug, slug, kind, label, interval_days, active_months, sort_order)
SELECT slug, 'repot', 'repot', 'Repot', repot_interval_days, '{}', 2
FROM species WHERE repot_interval_days IS NOT NULL;

ALTER TABLE species
  ADD COLUMN description text NOT NULL DEFAULT '',
  DROP COLUMN prune_interval_days,
  DROP COLUMN prune_months,
  DROP COLUMN fert_interval_days,
  DROP COLUMN fert_months,
  DROP COLUMN repot_interval_days;

-- care_events: task_slug is nullable and stays NULL on every row above this
-- migration. Backfilling would mean guessing which task an old "prune" event
-- satisfied, and for a species with two prune tasks the guess is undecidable.
-- A NULL slug means "logged before tasks had identity"; the engine treats it
-- as satisfying any task of the event's kind (SPEC §7.7).
ALTER TABLE care_events ADD COLUMN task_slug text;
DROP INDEX care_events_plant_id_kind_done_at_idx;
CREATE INDEX ON care_events (plant_id, kind, task_slug, done_at DESC);

-- care_tasks re-keys from (plant_id, kind) to (plant_id, task_slug). The
-- mapping is exact for the same reason as above: kind and slug coincide for
-- every task that exists before this migration.
ALTER TABLE care_tasks RENAME COLUMN kind TO task_slug;
ALTER TABLE care_tasks DROP CONSTRAINT care_tasks_plant_id_kind_key;
ALTER TABLE care_tasks ADD CONSTRAINT care_tasks_plant_id_task_slug_key UNIQUE (plant_id, task_slug);
