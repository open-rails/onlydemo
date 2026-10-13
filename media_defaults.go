package main

import (
	"context"
	"fmt"

	"github.com/open-rails/contentkit/media/image"
	"github.com/open-rails/contentkit/media/layout"
)

// publishMediaDefaults renders the media kinds' embedded default images (a
// person's avatar until they upload one) into the bucket and returns the
// gateway's MEDIA_GATEWAY_DEFAULTS. Objects are content-addressed and
// create-only, so reruns and overlapping deploys are safe. Needs libvips.
func publishMediaDefaults(ctx context.Context, cfg Config) (string, error) {
	reg, err := workerRegistry(cfg)
	if err != nil {
		return "", err
	}
	store, err := openMediaStore(ctx, cfg.Media)
	if err != nil {
		return "", err
	}
	published, err := image.PublishDefaults(ctx, store, reg)
	if err != nil {
		return "", fmt.Errorf("publish media defaults: %w", err)
	}
	return layout.FormatDefaults(published.Defaults), nil
}
