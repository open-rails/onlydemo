package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

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
	kindUser    = "user"
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

// contentKitConfig is contentkit.yaml plus the schemas, offer PSPs and media.
func contentKitConfig(cfg Config) (contentkit.Config, error) {
	var ck contentkit.Config
	dec := yaml.NewDecoder(bytes.NewReader(contentModel))
	dec.KnownFields(true)
	if err := dec.Decode(&ck); err != nil {
		return ck, fmt.Errorf("contentkit.yaml: %w", err)
	}
	ck.Database = contentkit.DatabaseConfig{Schema: contentSchema(cfg), RiverSchema: riverSchema(cfg)}
	ck.Offers.PSPs = slices.Sorted(maps.Keys(cfg.Merchant.PSPs))
	mc := cfg.Media
	kinds, err := mediaKinds(ck)
	if err != nil {
		return ck, err
	}
	ck.Media = &contentkit.MediaConfig{
		Config:       media.Config{BaseURL: mc.URL, Kinds: kinds, Avatars: media.Avatars{Kind: kindUser, Preset: media.AvatarPreset}},
		WorkerSchema: mediaWorkerSchema(cfg), ProcessOnUpload: true,
		Delivery: contentkit.DeliveryConfig{Mode: string(mc.Delivery), CookieDomain: mc.CookieDomain, SigningKey: mc.TokenKey, PreviousKey: mc.TokenKeyPrevious},
	}
	return ck, nil
}

// mediaKinds are the app's media kinds: a post's mixed images and videos
// (its version's media), a channel's cover banner and avatar, and people's
// avatars.
func mediaKinds(ck contentkit.Config) ([]media.Kind, error) {
	gk, ok := ck.Content.GroupKinds[kindChannel]
	if !ok {
		return nil, errors.New("contentkit.yaml: the channel group kind is required")
	}
	channel := media.GroupKind(kindChannel, gk)
	for i := range channel.Public {
		if channel.Public[i].Name == media.CoverPreset {
			channel.Public[i].Widths = []int{640, 1280, 1920}
			channel.Public[i].Image.Aspect, channel.Public[i].Image.MinWidth = media.Ratio("3:1"), 600
		}
	}
	user := avatar.User()
	user.Namespace = ""
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
		user,
	}, nil
}

// workerRegistry is the engine's media registry, for the media worker.
func workerRegistry(cfg Config) (*media.Registry, error) {
	ck, err := contentKitConfig(cfg)
	if err != nil {
		return nil, err
	}
	mc, err := media.ForContent(ck.Media.Config, ck.Content)
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
	return contentkit.New(ctx, ck, contentkit.Deps{Postgres: pool, Auth: auth.client.Authenticator(), Users: auth.client.UserInfo(),
		Scopes: ckauthkit.Scopes(auth.client), Billing: ckopenrails.Billing(billing.client), Store: store})
}

func unique(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
