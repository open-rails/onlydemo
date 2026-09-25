-- parent: 1 sha256:470ba50c48a1e6fd6f05fe324fa0e8a78bb4e17b9e618123733347b9b6f12b94

-- A channel optionally sells one membership. The catalog owns its price; this
-- row owns whether it is open, closed or free. Catalog writes run after commit
-- and the revision orders queued syncs, as for post offers.
ALTER TABLE channels
    ADD COLUMN membership TEXT NOT NULL DEFAULT 'none' CHECK (membership IN ('none','open','closed')),
    ADD COLUMN membership_free BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN membership_sync TEXT NOT NULL DEFAULT 'none' CHECK (membership_sync IN ('none','pending','active','failed')),
    ADD COLUMN membership_revision BIGINT NOT NULL DEFAULT 0;
