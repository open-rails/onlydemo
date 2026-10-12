-- Posts stay the app's until ContentKit's catalog stores them (#117 phase B).
-- Channels are ContentKit groups: channel_id names one, with no foreign key.
CREATE TABLE posts (
    -- A UUIDv7: the media folder and the post's content key.
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id UUID NOT NULL,
    author_id UUID NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    access_policy TEXT NOT NULL DEFAULT 'public' CHECK (access_policy IN ('public','membership','members_ppv','ppv')),
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','published')),
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Deleted posts stay readable to their buyers.
    deleted_at TIMESTAMPTZ,
    CONSTRAINT posts_state_published_at CHECK ((state = 'published') = (published_at IS NOT NULL))
);

CREATE INDEX posts_channel_id_idx ON posts (channel_id, id DESC);
