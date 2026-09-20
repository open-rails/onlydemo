-- parent: 1 sha256:d5f04c3e5a60ab6d6beb2a47b39c01b25a00001169cfa3844e6b0e430fc23b64

ALTER TABLE blog_posts
    ADD COLUMN billing_key UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN price_cents BIGINT,
    ADD COLUMN openrails_product_id TEXT,
    ADD COLUMN openrails_price_id TEXT,
    ADD CONSTRAINT blog_posts_price_check CHECK (
        price_cents IS NULL OR (visibility = 'private' AND price_cents BETWEEN 50 AND 99999999)
    );

-- OpenRails owns its catalog and purchase grants. These are opaque references,
-- retained when the author stops selling a post so existing purchases still work.
CREATE UNIQUE INDEX blog_posts_product_id_idx ON blog_posts (openrails_product_id)
    WHERE openrails_product_id IS NOT NULL;

CREATE UNIQUE INDEX blog_posts_billing_key_idx ON blog_posts (billing_key);
