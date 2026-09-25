-- parent: 7 sha256:9ba9d828f304e06b92670d5bd9cbe7886346c0b0a71bddd5021e8d93d92bff49

-- A published draft whose media is still processing is 'publishing': only its
-- channel's editors see it until ContentKit's ItemReady hook makes it
-- 'published'. published_at is when it went live.
ALTER TABLE posts ADD COLUMN state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','publishing','published'));
UPDATE posts SET state = 'published' WHERE published_at IS NOT NULL;
ALTER TABLE posts
    DROP CONSTRAINT posts_published_slug,
    ADD CONSTRAINT posts_state_slug CHECK (state = 'draft' OR slug IS NOT NULL),
    ADD CONSTRAINT posts_state_published_at CHECK ((state = 'published') = (published_at IS NOT NULL));
DROP INDEX posts_drafts_idx;
CREATE INDEX posts_drafts_idx ON posts (created_at) WHERE state = 'draft';
