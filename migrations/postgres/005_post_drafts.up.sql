-- parent: 4 sha256:8902258943934498f3dce4ea9b728fc27b17bddac50743ea152b9d0366869451

-- A composer creates its post as a draft so uploads have a folder; publishing
-- sets published_at, and drafts get their slug then.
ALTER TABLE posts ADD COLUMN published_at TIMESTAMPTZ;
UPDATE posts SET published_at = created_at;
ALTER TABLE posts ALTER COLUMN slug DROP NOT NULL;
ALTER TABLE posts ADD CONSTRAINT posts_published_slug CHECK (published_at IS NULL OR slug IS NOT NULL);
CREATE INDEX posts_drafts_idx ON posts (created_at) WHERE published_at IS NULL;
