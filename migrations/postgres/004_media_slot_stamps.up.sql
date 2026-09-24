-- parent: 3 sha256:3c43172a0e96847d94664584cd289beb20199378b0b8741be631f8309383eb90

-- ContentKit's SlotStamp ("{version}:{w},{w}") replaces version + widths: one
-- value per slot builds every output URL.
ALTER TABLE media_slots ADD COLUMN stamp TEXT;
UPDATE media_slots SET stamp = version || ':' || array_to_string(widths, ',');
ALTER TABLE media_slots ALTER COLUMN stamp SET NOT NULL, DROP COLUMN version, DROP COLUMN widths;
