-- parent: root

-- AuthKit owns membership and owner invariants. This row owns application
-- content/catalog lifecycle and deliberately has no foreign key into AuthKit.
CREATE TABLE channels (
    id UUID PRIMARY KEY,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE blog_posts (
    id BIGSERIAL PRIMARY KEY,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author_id UUID NOT NULL,
    billing_key UUID NOT NULL DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    access_policy TEXT NOT NULL DEFAULT 'public' CHECK (access_policy IN ('public','membership','members_ppv','ppv')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX blog_posts_channel_id_idx ON blog_posts (channel_id);
CREATE UNIQUE INDEX blog_posts_billing_key_idx ON blog_posts (billing_key);
