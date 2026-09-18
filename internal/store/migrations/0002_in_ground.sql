ALTER TABLE plants DROP CONSTRAINT plants_pot_diameter_mm_check;
ALTER TABLE plants ALTER COLUMN pot_diameter_mm DROP NOT NULL;
-- CHECK treats UNKNOWN as pass, so BETWEEN on a NULL diameter would allow indoor in-ground.
ALTER TABLE plants ADD CONSTRAINT plants_pot_check CHECK (
  (pot_diameter_mm IS NULL AND location = 'outdoor')
  OR (pot_diameter_mm IS NOT NULL AND pot_diameter_mm BETWEEN 40 AND 2000)
);
