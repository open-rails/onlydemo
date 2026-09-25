-- parent: 6 sha256:5582a6bfcac165f364a1db484e9141b9a0c7a13e766aed5de1dc1143c79b4d6f

-- ContentKit v0.48 names slot outputs by content hash: a row stores the
-- slot's SlotListing (aspect, outputs) from Hooks.SlotEncoded so listings
-- link it without reads. Old rows point at the retired layout.
DELETE FROM media_slots;
ALTER TABLE media_slots DROP COLUMN aspect, ADD COLUMN listing JSONB NOT NULL;
