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
    visibility TEXT NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('public', 'private')),
    price_cents BIGINT,
    openrails_product_id TEXT,
    openrails_price_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT blog_posts_price_check CHECK (
        price_cents IS NULL OR (visibility = 'private' AND price_cents BETWEEN 50 AND 99999999)
    )
);

CREATE INDEX blog_posts_channel_id_idx ON blog_posts (channel_id);
CREATE INDEX blog_posts_public_created_at_idx
    ON blog_posts (created_at DESC)
    WHERE visibility = 'public';

-- These opaque catalog references survive delisting so purchased access remains.
CREATE UNIQUE INDEX blog_posts_product_id_idx ON blog_posts (openrails_product_id)
    WHERE openrails_product_id IS NOT NULL;
CREATE UNIQUE INDEX blog_posts_billing_key_idx ON blog_posts (billing_key);
