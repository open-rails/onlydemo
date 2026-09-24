-- parent: 2 sha256:ebbce1e14f06b8a7c745a1364970508907f6fb7ced30461bc386d04854d505d3

-- The current encode of each public slot (avatar, cover), recorded by
-- ContentKit's SlotEncoded hook, so listings build versioned srcsets without
-- reading the bucket. No row: the slot was never set.
CREATE TABLE media_slots (
    kind TEXT NOT NULL,
    item_id TEXT NOT NULL,
    slot TEXT NOT NULL,
    version TEXT NOT NULL,
    widths INT[] NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (kind, item_id, slot)
);
