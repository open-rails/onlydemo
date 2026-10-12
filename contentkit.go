package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit"
	"github.com/open-rails/contentkit/access"
	ckopenrails "github.com/open-rails/contentkit/adapters/openrails"
	"github.com/open-rails/contentkit/content"
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
// (its version media once posts are ContentKit content), a channel's cover
// banner and avatar, and people's avatars.
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

// workerRegistry is the engine's media registry for the media worker. Posts
// stay a plain folder kind until ContentKit stores them (#120).
func workerRegistry(cfg Config) (*media.Registry, error) {
	ck, err := contentKitConfig(cfg)
	if err != nil {
		return nil, err
	}
	site := ck.Content
	site.Kinds = maps.Clone(site.Kinds)
	delete(site.Kinds, content.KindPost)
	mc, err := media.ForContent(ck.Media.Config, site)
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
// Posts are still the app's (posts.go answers their media) until ContentKit's
// catalog stores them.
func newContentKit(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, billing *billingService, posts *postAPI) (*contentkit.Engine, error) {
	ck, err := contentKitConfig(cfg)
	if err != nil {
		return nil, err
	}
	store, err := openMediaStore(ctx, cfg.Media)
	if err != nil {
		return nil, err
	}
	pm := postMedia{posts}
	return contentkit.New(ctx, ck, contentkit.Deps{Postgres: pool, Auth: auth.client.Authenticator(), Users: auth.client.UserInfo(),
		Billing: ckopenrails.Billing(billing.client), Store: store, Resolver: pm, CanUpload: pm})
}

// newGate decides the app's posts against the viewer's keys, as ContentKit
// decides its own content.
func newGate(cfg contentkit.Config, billing *billingService) (*access.Gate, error) {
	return access.NewGate(access.GateConfig{Tenant: cfg.Tenant, Kinds: cfg.Content, Billing: ckopenrails.Billing(billing.client)})
}

// ckAPI calls ContentKit's HTTP API in-process: open routes anonymously, the
// rest with the caller's own credential.
type ckAPI struct {
	hc *http.Client
}

func newCKAPI(h http.Handler) *ckAPI {
	return &ckAPI{hc: &http.Client{Transport: handlerTransport{h}}}
}

type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

// ckError is a refused ContentKit call.
type ckError struct {
	Status int
	Code   string `json:"code"`
	Msg    string `json:"error"`
}

func (e *ckError) Error() string {
	return fmt.Sprintf("contentkit: %d %s: %s", e.Status, e.Code, e.Msg)
}

func ckStatus(err error) int {
	var e *ckError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// call sends in as JSON to path under the API (/v1/...) with credential (an
// Authorization header value, or "") and decodes the reply into out.
func (a *ckAPI) call(ctx context.Context, method, path, credential string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://contentkit"+contentkitPrefix+path, body)
	if err != nil {
		return err
	}
	req.RemoteAddr = "127.0.0.1:0"
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", credential)
	}
	res, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		e := &ckError{Status: res.StatusCode}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(e)
		return e
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// group is the part of ContentKit's GroupDetail the app reads.
type group struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	State       string `json:"state"`
	OwnerUserID string `json:"owner_user_id"`
	AuthScope   string `json:"auth_scope"`
}

// channels reads the active channels among ids; missing ones are absent.
func (a *ckAPI) channels(ctx context.Context, ids []string) (map[string]group, error) {
	out := map[string]group{}
	for _, id := range unique(ids) {
		var g group
		err := a.call(ctx, http.MethodGet, "/v1/groups/"+id, "", nil, &g)
		switch {
		case ckStatus(err) == http.StatusNotFound || ckStatus(err) == http.StatusBadRequest:
		case err != nil:
			return nil, err
		case g.Kind == kindChannel && g.State == "active" && g.ID == id:
			out[id] = g
		}
	}
	return out, nil
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
