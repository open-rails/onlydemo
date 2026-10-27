package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit"
	ckauthkit "github.com/open-rails/contentkit/adapters/authkit"
	ckopenrails "github.com/open-rails/contentkit/adapters/openrails"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/contentkit/media/avatar"
	"gopkg.in/yaml.v3"
)

// tenant is the app's ContentKit tenant: the media namespace and the prefix
// of every access key.
const tenant = "onlydemo"

// contentkitPrefix is where ContentKit's API is mounted: /contentkit/v1/...
const contentkitPrefix = "/contentkit"

// mediaWorkerSchema holds the media worker's queue, beside ContentKit's schema.
func mediaWorkerSchema(cfg Config) string { return contentSchema(cfg) + "_media_worker" }

const (
	kindPost    = "post"
	kindChannel = "channel"
)

//go:embed contentkit.yaml
var contentModel []byte

const (
	postImages    = "images/{name}"
	postVideos    = "videos/{name}"
	maxImageBytes = 25 << 20
	maxVideoBytes = 20 << 30 // room for long 4K sources; multipart
	maxPostImages = 40
	maxPostVideos = 10
)

var (
	imageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/avif"}
	videoTypes = []string{"video/mp4", "video/webm", "video/quicktime", "video/x-matroska"}
)

// contentKitConfig is contentkit.yaml plus the schemas and media.
func contentKitConfig(cfg Config) (contentkit.Config, error) {
	var ck contentkit.Config
	dec := yaml.NewDecoder(bytes.NewReader(contentModel))
	dec.KnownFields(true)
	if err := dec.Decode(&ck); err != nil {
		return ck, fmt.Errorf("contentkit.yaml: %w", err)
	}
	ck.Database = contentkit.DatabaseConfig{Schema: contentSchema(cfg), RiverSchema: riverSchema(cfg)}
	mc := cfg.Media
	kinds, err := mediaKinds(ck)
	if err != nil {
		return ck, err
	}
	ck.Media = &contentkit.MediaConfig{
		Config:       media.Config{BaseURL: mc.URL, Kinds: kinds},
		WorkerSchema: mediaWorkerSchema(cfg), ProcessOnUpload: true,
		Delivery: contentkit.DeliveryConfig{Mode: string(mc.Delivery), CookieDomain: mc.CookieDomain, SigningKey: mc.TokenKey, PreviousKey: mc.TokenKeyPrevious},
	}
	user := avatar.User()
	user.Namespace = ""
	ck.Avatars = &contentkit.AvatarsConfig{Kind: &user}
	return ck, nil
}

// mediaKinds are posts' mixed media and channels' image slots.
func mediaKinds(ck contentkit.Config) ([]media.Kind, error) {
	if ck.Content.Channels == nil {
		return nil, errors.New("contentkit.yaml: channels must be configured")
	}
	channel := media.EntityKind(kindChannel, ck.Content.StorageModel().EntityKinds[kindChannel])
	for i := range channel.Public {
		if channel.Public[i].Name == media.CoverPreset {
			channel.Public[i].Widths = []int{640, 1280, 1920}
			channel.Public[i].Image.Aspect, channel.Public[i].Image.MinWidth = media.Ratio("3:1"), 600
		}
	}
	return []media.Kind{
		{Name: kindPost, KeepOriginals: true,
			Uploads: []media.Upload{
				{Path: postImages, Types: imageTypes, MaxBytes: maxImageBytes, Max: maxPostImages},
				{Path: postVideos, Types: videoTypes, MaxBytes: maxVideoBytes, Max: maxPostVideos},
			},
			Private: []media.Private{
				{Name: "large", From: postImages, To: "large/{name}.webp", Image: &media.Image{Width: 1600, Height: 1600, Quality: 85}},
				{Name: "hls", From: postVideos, To: "hls/{name}/", HLS: &media.HLS{}},
			},
			// A post's first image, blurred: public wherever the post is.
			Public: []media.Public{{Name: "preview", From: postImages, To: "preview-{n}.webp", First: 1,
				Image: media.Image{Width: 960, Height: 960, Quality: 70, Blur: 24}}}},
		channel,
	}, nil
}

// workerRegistry is the engine's media registry, for the media worker.
func workerRegistry(cfg Config) (*media.Registry, error) {
	ck, err := contentKitConfig(cfg)
	if err != nil {
		return nil, err
	}
	mc, err := ck.MediaKinds()
	if err != nil {
		return nil, err
	}
	mc.Namespace = tenant
	mc.Hooks = media.Hooks{Failed: mediaFailed}
	return media.NewRegistry(mc)
}

// mediaFailed is the worker's Hooks.Failed: a refused upload is shown to its
// editor, a processing fault is logged.
func mediaFailed(_ context.Context, ref contentref.ContentRef, file string, err error) {
	if ie := media.AsImageError(err); ie != nil {
		slog.Info("media file refused", "ref", ref.String(), "file", file, "code", ie.Code, "reason", ie.Message)
		return
	}
	slog.Warn("media file cannot be derived", "ref", ref.String(), "file", file, "err", err)
}

// newContentKit runs ContentKit in this process; New migrates its schemas.
func newContentKit(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, billing *billingService) (*contentkit.Engine, error) {
	ck, err := contentKitConfig(cfg)
	if err != nil {
		return nil, err
	}
	store, err := openMediaStore(ctx, cfg.Media)
	if err != nil {
		return nil, err
	}
	notifications, err := auth.client.Notifications("contentkit")
	if err != nil {
		return nil, err
	}
	return contentkit.New(ctx, ck, contentkit.Deps{Postgres: pool, Auth: auth.client.Authenticator(), Users: auth.client.UserInfo(),
		Scopes: ckauthkit.Scopes(auth.client), Billing: ckopenrails.Billing(billing.client), Store: store, Notifications: notifications})
}
