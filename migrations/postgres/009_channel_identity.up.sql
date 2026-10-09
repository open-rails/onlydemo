-- parent: 8 sha256:7b32b242aa7d6be9804fe7adf0d5f96aff7b39b20920338b03aeff8ca2244120
-- A channel's address and display name are the app's; AuthKit holds only its
-- permission group, whose id is the channel's.
ALTER TABLE channels
    ADD COLUMN slug TEXT NOT NULL,
    ADD COLUMN name TEXT NOT NULL;

-- A deleted channel keeps its slug until it is purged.
CREATE UNIQUE INDEX channels_slug_idx ON channels (slug);
