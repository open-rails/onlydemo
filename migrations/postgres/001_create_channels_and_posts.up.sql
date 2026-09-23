-- parent: root

-- AuthKit owns membership and owner invariants. This row owns application
-- content/catalog lifecycle and deliberately has no foreign key into AuthKit.
CREATE TABLE channels (
    id UUID PRIMARY KEY,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE posts (
    id BIGSERIAL PRIMARY KEY,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author_id UUID NOT NULL,
    billing_key UUID NOT NULL DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    access_policy TEXT NOT NULL DEFAULT 'public' CHECK (access_policy IN ('public','membership','members_ppv','ppv')),
    -- Catalog writes run after commit; the revision orders queued offer syncs.
    offer_status TEXT NOT NULL DEFAULT 'none' CHECK (offer_status IN ('none','pending','active','failed')),
    offer_revision BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Deleted posts stay for purchase history and entitlements keyed by billing_key.
    deleted_at TIMESTAMPTZ
);

CREATE INDEX posts_channel_id_idx ON posts (channel_id);
CREATE UNIQUE INDEX posts_billing_key_idx ON posts (billing_key);
CREATE UNIQUE INDEX posts_live_slug_idx ON posts (slug) WHERE deleted_at IS NULL;
