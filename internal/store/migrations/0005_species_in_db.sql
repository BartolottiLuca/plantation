-- The species catalog stops being rebuilt from YAML at every boot and becomes
-- ordinary rows, created and edited in the app. Nothing is seeded: a fresh
-- database starts with an empty catalog. On a database that has been booting
-- from YAML the existing species stay exactly as they are — plants reference
-- them — and from here on they are ordinary, editable rows.
--
-- origin, ai_model and ai_drafted_at record who wrote a row's numbers. Rows that
-- predate this migration were written by a person, so the 'manual' default is
-- honest for them too.
ALTER TABLE species
  ADD COLUMN origin         text NOT NULL DEFAULT 'manual'
    CHECK (origin IN ('ai', 'manual')),
  ADD COLUMN ai_model       text,
  ADD COLUMN ai_drafted_at  timestamptz,
  -- Where a plant lives is a fact about the plant (plants.location), not its
  -- species: a lavender on a windowsill and one in a bed are the same species.
  DROP COLUMN placement;

-- A task that only makes sense in one place (mulching a bed) names it. NULL
-- means the task applies wherever the plant is, which is every existing task.
ALTER TABLE species_tasks
  ADD COLUMN only_in text CHECK (only_in IN ('indoor', 'outdoor'));
