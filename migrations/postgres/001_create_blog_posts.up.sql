-- parent: root

CREATE TABLE blog_posts (
    id BIGSERIAL PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES profiles.users (id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    visibility TEXT NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('public', 'private')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX blog_posts_owner_id_idx ON blog_posts (owner_id);
CREATE INDEX blog_posts_public_created_at_idx
    ON blog_posts (created_at DESC)
    WHERE visibility = 'public';
