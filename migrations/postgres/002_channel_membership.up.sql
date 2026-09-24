-- parent: 1 sha256:8c08588eec8b39d467f46381a12798e42a3035e87a8a5d79d2e980459cd9b1c6

-- A channel optionally sells one membership. The catalog owns its price; this
-- row owns whether it is open, closed or free. Catalog writes run after commit
-- and the revision orders queued syncs, as for post offers.
ALTER TABLE channels
    ADD COLUMN membership TEXT NOT NULL DEFAULT 'none' CHECK (membership IN ('none','open','closed')),
    ADD COLUMN membership_free BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN membership_sync TEXT NOT NULL DEFAULT 'none' CHECK (membership_sync IN ('none','pending','active','failed')),
    ADD COLUMN membership_revision BIGINT NOT NULL DEFAULT 0;
