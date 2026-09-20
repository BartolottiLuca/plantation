-- Pot diameter moves from millimeters to centimeters: what a person measures
-- with a tape, not the unit the reservoir model happens to compute in.
-- care.Effective converts back to mm (SPEC §7.1) for the water balance.
ALTER TABLE plants DROP CONSTRAINT plants_pot_check;
ALTER TABLE plants RENAME COLUMN pot_diameter_mm TO pot_diameter_cm;
-- Existing rows were entered in mm; round rather than truncate so a
-- non-round value (e.g. 185mm from before this migration existed) doesn't
-- silently lose half a centimetre.
UPDATE plants SET pot_diameter_cm = ROUND(pot_diameter_cm / 10.0) WHERE pot_diameter_cm IS NOT NULL;
ALTER TABLE plants ADD CONSTRAINT plants_pot_check CHECK (
  (pot_diameter_cm IS NULL AND location = 'outdoor')
  OR (pot_diameter_cm IS NOT NULL AND pot_diameter_cm BETWEEN 4 AND 200)
);
