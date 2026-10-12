package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	helpersauth "github.com/open-rails/authkit/auth"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/authkit/verify"
	"github.com/open-rails/contentkit"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/contentkit/content"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/contenturl"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/contentkit/media/token"
	"github.com/open-rails/contentkit/media/workqueue"
	"github.com/open-rails/contentkit/migrations"
	"golang.org/x/sync/errgroup"
)

// tenant is the app's ContentKit tenant: the media namespace, content refs,
// content codes and entitlement keys.
const tenant = "onlydemo"

// mediaWorkerSchema holds the media worker's River jobs, apart from the app's.
const mediaWorkerSchema = "onlydemo_media_worker"

const (
	kindPost    = "post"
	kindChannel = "channel"
	kindUser    = "user"
)

// Upload paths. ContentKit orders uploads within each path, so a post shows
// its images, then its videos.
const (
	postImages = "images/{name}"
	postVideos = "videos/{name}"
	avatarPath = "avatar"
	coverPath  = "cover"
)

const (
	maxImageBytes = 25 << 20
	maxVideoBytes = 20 << 30 // room for long 4K sources; multipart
	maxPostImages = 40
	maxPostVideos = 10
)

var (
	imageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/avif"}
	videoTypes = []string{"video/mp4", "video/webm", "video/quicktime", "video/x-matroska"}
	// Avatars show at 40–96 px, channel covers up to ~1000 px wide (×2–3 density).
	avatarImage = media.Public{Name: "avatar", From: avatarPath, To: "avatar-{w}.webp", Widths: []int{128, 512},
		Image: media.Image{Aspect: media.Ratio("1:1"), MinWidth: 128, Quality: 85}}
	coverImage = media.Public{Name: "cover", From: coverPath, To: "cover-{w}.webp", Widths: []int{900, 3000},
		Image: media.Image{Aspect: media.Ratio("3:1"), MinWidth: 600, Quality: 85}}
	// previewImage is a post's first image, blurred: public to everyone who
	// can see the post; everything else of a paid post stays private.
	previewImage = media.Public{Name: "preview", From: postImages, To: "preview-{n}.webp", First: 1,
		Image: media.Image{Width: 960, Height: 960, Quality: 70, Blur: 24}}
)

func mediaKinds() []media.Kind {
	image := func(path string) media.Upload {
		return media.Upload{Path: path, Types: imageTypes, MaxBytes: maxImageBytes}
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
			Public: []media.Public{previewImage}},
		{Name: kindChannel, KeepOriginals: true, Uploads: []media.Upload{image(avatarPath), image(coverPath)},
			Public: []media.Public{avatarImage, coverImage}},
		{Name: kindUser, KeepOriginals: true, Uploads: []media.Upload{image(avatarPath)}, Public: []media.Public{avatarImage}},
	}
}

func newRegistry(mc mediaConfig, hooks media.Hooks) (*media.Registry, error) {
	return media.NewRegistry(media.Config{Namespace: tenant, BaseURL: mc.URL, Kinds: mediaKinds(), Hooks: hooks})
}

type mediaConfig struct {
	S3Endpoint       string
	S3PublicEndpoint string
	S3Bucket         string
	S3Region         string
	S3AccessKeyID    string
	S3SecretKey      string
	URL              string // media-gateway origin
	Delivery         media.DeliveryMode
	CookieDomain     string
	TokenKey         string
	TokenKeyPrevious string
	FilesPerHour     int
	BytesPerDay      int64
	ChannelQuota     int64
	// temp/ retention and the sweep pass interval; zero keeps ContentKit's
	// defaults (48 h, 24 h).
	TempTTL       time.Duration
	SweepInterval time.Duration
}

func loadMediaConfig(get func(string) string) (mediaConfig, error) {
	or := func(key, def string) string {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v
		}
		return def
	}
	c := mediaConfig{
		S3Endpoint:       or("media_s3_endpoint", ""),
		S3Bucket:         or("media_s3_bucket", "onlydemo-media"),
		S3Region:         or("media_s3_region", "us-east-1"),
		S3AccessKeyID:    or("media_s3_access_key_id", ""),
		S3SecretKey:      or("media_s3_secret_access_key", ""),
		URL:              strings.TrimRight(or("media_url", ""), "/"),
		Delivery:         media.DeliveryMode(or("media_delivery", string(media.DeliverCookie))),
		CookieDomain:     or("media_cookie_domain", ""),
		TokenKey:         or("media_token_key", ""),
		TokenKeyPrevious: or("media_token_key_previous", ""),
	}
	c.S3PublicEndpoint = or("media_s3_public_endpoint", c.S3Endpoint)
	var err error
	num := func(key string, def int64) int64 {
		raw := or(key, "")
		if raw == "" || err != nil {
			return def
		}
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < 0 {
			err = fmt.Errorf("%s must be a non-negative integer", strings.ToUpper(key))
		}
		return n
	}
	c.FilesPerHour = int(num("media_upload_files_per_hour", 60))
	c.BytesPerDay = num("media_upload_bytes_per_day", 100<<30)
	c.ChannelQuota = num("media_channel_quota_bytes", 500<<30)
	dur := func(key string) time.Duration {
		raw := or(key, "")
		if raw == "" || err != nil {
			return 0
		}
		d, e := time.ParseDuration(raw)
		if e != nil || d < 0 {
			err = fmt.Errorf("%s must be a non-negative duration", strings.ToUpper(key))
		}
		return d
	}
	c.TempTTL = dur("media_temp_ttl")
	c.SweepInterval = dur("media_sweep_interval")
	if err != nil {
		return c, err
	}
	for name, v := range map[string]string{"MEDIA_S3_ENDPOINT": c.S3Endpoint, "MEDIA_S3_ACCESS_KEY_ID": c.S3AccessKeyID,
		"MEDIA_S3_SECRET_ACCESS_KEY": c.S3SecretKey, "MEDIA_URL": c.URL, "MEDIA_TOKEN_KEY": c.TokenKey} {
		if v == "" {
			return c, fmt.Errorf("%s is required", name)
		}
	}
	return c, nil
}

// mediaService is ContentKit media wired to this app: AuthKit decides uploads,
// the post access policy decides reads, OpenRails holds the entitlements.
type mediaService struct {
	cfg       mediaConfig
	store     media.Store
	kinds     *media.Registry
	manifests *media.Manifests
	jobs      *media.Jobs
	uploads   *media.Uploads
	reader    *media.Reader
	limiter   *media.PGLimiter
	resolver  access.ContentResolver
	pool      *pgxpool.Pool
	auth      *appAuth
	billing   *billingService
	channels  *channelAPI
	posts     *postAPI
}

// applyContentMigrations installs ContentKit's schema (upload limiter,
// content codes) and the media worker's River schema.
func applyContentMigrations(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	if err := migrations.ApplyPostgres(ctx, db, contentSchema(cfg)); err != nil {
		return err
	}
	return workqueue.Migrate(ctx, pool, mediaWorkerSchema)
}

// mediaFailed is Hooks.Failed: a refused upload is shown to its editor, a
// processing fault is logged.
func mediaFailed(_ context.Context, ref contentref.ContentRef, file string, err error) {
	if ie := media.AsImageError(err); ie != nil {
		slog.Info("media file refused", "ref", ref.String(), "file", file, "code", ie.Code, "reason", ie.Message)
		return
	}
	slog.Warn("media file cannot be derived", "ref", ref.String(), "file", file, "err", err)
}

// openMediaStore opens the bucket and checks it answers.
func openMediaStore(ctx context.Context, mc mediaConfig) (*mediaS3.Store, error) {
	store, err := mediaS3.New(mediaS3.Config{Bucket: mc.S3Bucket, Region: mc.S3Region, Endpoint: mc.S3Endpoint, PublicEndpoint: mc.S3PublicEndpoint,
		AccessKeyID: mc.S3AccessKeyID, SecretAccessKey: mc.S3SecretKey, UsePathStyle: true})
	if err != nil {
		return nil, err
	}
	if err := store.Check(ctx, "_probe/"); err != nil {
		return nil, fmt.Errorf("media bucket %s: %w", mc.S3Bucket, err)
	}
	return store, nil
}

func newMedia(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, billing *billingService, channels *channelAPI, posts *postAPI) (*mediaService, error) {
	mc := cfg.Media
	store, err := openMediaStore(ctx, mc)
	if err != nil {
		return nil, err
	}
	ring, err := token.ParseRing(mc.TokenKey, mc.TokenKeyPrevious)
	if err != nil {
		return nil, fmt.Errorf("MEDIA_TOKEN_KEY: %w", err)
	}
	signing, _ := token.ParseKey(mc.TokenKey)
	m := &mediaService{cfg: mc, store: store, auth: auth, billing: billing, channels: channels, posts: posts, pool: pool}
	// Drafts and publishing posts are hidden from anonymous viewers, so their
	// previews are never public before they are. Paid posts keep a preview.
	m.resolver = billing.gate.Resolver(m)
	if m.kinds, err = newRegistry(mc, media.Hooks{Resolver: m.resolver, CanUpload: m, ItemReady: m.postReady}); err != nil {
		return nil, err
	}
	m.limiter, err = media.NewPGLimiter(pool, contentSchema(cfg), media.PGLimits{
		FilesPerHour: mc.FilesPerHour, BytesPerDay: mc.BytesPerDay,
		Quota: func(_ context.Context, _, owner string) (int64, error) {
			if strings.HasPrefix(owner, kindChannel+":") {
				return mc.ChannelQuota, nil
			}
			return 0, nil
		},
	})
	if err != nil {
		return nil, err
	}
	// Processing (placement, images, video) runs in `onlydemo media-worker`;
	// the app only enqueues. Files process as soon as they upload.
	queue, err := workqueue.New(pool, m.kinds, mediaWorkerSchema)
	if err != nil {
		return nil, err
	}
	// The journal records each manifest write with its quota and queue
	// effects in the app's database, so an interrupted one is recovered.
	journal, err := media.NewPGJournal(pool, contentSchema(cfg), queue)
	if err != nil {
		return nil, err
	}
	if m.jobs, err = media.NewJobs(media.JobsConfig{Store: store, Registry: m.kinds, Locker: media.PGLocker(pool), Journal: journal, Processes: queue,
		Pool: pool, Limiter: m.limiter, TempTTL: mc.TempTTL, SweepInterval: mc.SweepInterval}); err != nil {
		return nil, err
	}
	m.manifests = m.jobs.Manifests()
	if m.uploads, err = media.NewUploads(media.UploadOptions{Store: store, Manifests: m.manifests, Tickets: &ring,
		Limiter: m.limiter, ProcessOnUpload: true}); err != nil {
		return nil, err
	}
	progress, err := workqueue.NewProgressSource(pool, mediaWorkerSchema)
	if err != nil {
		return nil, err
	}
	if m.reader, err = media.NewReader(media.ReaderOptions{Manifests: m.manifests, Queue: queue, Progress: progress,
		Delivery: media.Delivery{Mode: mc.Delivery, CookieDomain: mc.CookieDomain, SigningKey: signing}}); err != nil {
		return nil, err
	}
	channels.media, posts.media, auth.media = m, m, m
	return m, nil
}

func ref(kind, id string) contentref.ContentRef { return contentref.New(tenant, kind, id) }
func postRef(id string) contentref.ContentRef   { return ref(kindPost, id) }

// publicPreset is a public preset at an item, as the browser package's
// PublicPreset takes it: its published renditions, none before an upload.
type publicPreset struct {
	Preset     string                  `json:"preset"`
	Aspect     string                  `json:"aspect,omitempty"`
	Renditions []media.PublicRendition `json:"renditions"`
}

// publicImages answers presets ps of each item, reading the items' manifests
// concurrently.
func (m *mediaService) publicImages(ctx context.Context, kind string, ids []string, ps ...media.Public) (map[string][]publicPreset, error) {
	ids = unique(ids)
	published := make([][]media.PublicImage, len(ids))
	if m != nil {
		g, ctx := errgroup.WithContext(ctx)
		g.SetLimit(8)
		for i, id := range ids {
			g.Go(func() (err error) {
				published[i], err = m.manifests.PublicImages(ctx, ref(kind, id))
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}
	out := make(map[string][]publicPreset, len(ids))
	for i, id := range ids {
		for _, p := range ps {
			img := publicPreset{Preset: p.Name, Aspect: p.Image.Aspect.String(), Renditions: []media.PublicRendition{}}
			for _, pub := range published[i] {
				if pub.Preset == p.Name {
					img.Renditions = pub.Renditions
				}
			}
			out[id] = append(out[id], img)
		}
	}
	return out, nil
}

// createPost starts a new post's folder, hidden until Expose: a folder
// already holding objects (a reused id) refuses the post instead of showing
// another post's media.
func (m *mediaService) createPost(ctx context.Context, id string) error {
	if m == nil {
		return nil
	}
	_, err := m.manifests.Create(ctx, postRef(id))
	return err
}

func channelOwner(id string) string { return kindChannel + ":" + id }

func actorFor(user string) access.Actor {
	return access.Actor{ID: user, Kind: "user", Anonymous: user == ""}
}

// grants reads user's grants on a channel, root grants included, in one call.
func (m *mediaService) grants(ctx context.Context, user, channel string) (grants, error) {
	if user == "" {
		return nil, nil
	}
	perms, err := m.auth.effective(ctx, user, []string{channel})
	if err != nil {
		return nil, err
	}
	return perms[channel], nil
}

type grants []iam.Perm

func (g grants) can(perm iam.Perm) bool { return slices.ContainsFunc(g, perm.Matches) }

// postAccess loads a post on a live channel (found false otherwise) and the
// actor's upload grant and grants on its channel. Nobody uploads to a deleted
// post or edits it.
func (m *mediaService) postAccess(ctx context.Context, actor access.Actor, contentID string) (p post, up media.UploadGrant, g grants, found bool, err error) {
	id, err := parsePostID(contentID)
	if err != nil {
		return p, up, nil, false, nil
	}
	p, err = m.posts.onLiveChannel(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, up, nil, false, nil
	} else if err != nil {
		return p, up, nil, false, err
	}
	if p.Deleted {
		return p, up, nil, true, nil
	}
	if g, err = m.grants(ctx, actor.ID, p.ChannelID); err != nil {
		return p, up, nil, false, err
	}
	admin := g.can(postEditPermission)
	up = media.UploadGrant{Owner: channelOwner(p.ChannelID), Exempt: admin,
		Allowed: admin || (!p.draft() || p.AuthorID == actor.ID) && g.can(channelCreatePermission)}
	return p, up, g, true, nil
}

// Facts is the gate's port: what the app knows of each item for the actor.
// The gate turns them into media's resolution, deciding paid posts against
// the actor's entitlements. Channel editors and site admins read everything;
// whoever may upload to an item edits it (its editor views and edits).
func (m *mediaService) Facts(ctx context.Context, refs []contentref.ContentRef, actor access.Actor) (map[contentref.ContentKey]access.Fact, error) {
	out := make(map[contentref.ContentKey]access.Fact, len(refs))
	for _, r := range refs {
		f, ok, err := m.fact(ctx, r, actor)
		if err != nil {
			return nil, err
		}
		if ok {
			out[r.Key()] = f
		}
	}
	return out, nil
}

func (m *mediaService) fact(ctx context.Context, r contentref.ContentRef, actor access.Actor) (access.Fact, bool, error) {
	switch r.ContentKind {
	case kindPost:
		p, up, g, found, err := m.postAccess(ctx, actor, r.ContentID)
		if err != nil || !found {
			return access.Fact{}, false, err
		}
		if p.Deleted {
			// Only its buyers still see a deleted post; it has no public files.
			held, err := m.billing.access(ctx, actor.ID, []string{postResource(p.ID)})
			if err != nil {
				return access.Fact{}, false, err
			}
			return access.Fact{Visible: held[postResource(p.ID)], Rule: access.Rule{Level: access.PPV}}, true, nil
		}
		switch p.State {
		case stateDraft:
			// Only its author (or a site admin) sees a draft.
			return access.Fact{Visible: up.Allowed, Editor: up.Allowed}, true, nil
		case statePublishing:
			// Only its channel's editors see a post whose media is processing.
			editor := up.Allowed || g.can(channelEditPermission)
			return access.Fact{Visible: editor, Editor: up.Allowed, Bypass: editor}, true, nil
		}
		return access.Fact{Visible: true, Editor: up.Allowed, Rule: postRule(p),
			Bypass: g.can(channelReadPermission) || g.can(postReadPermission)}, true, nil
	case kindChannel, kindUser:
		// Public avatars and covers.
		f := access.Fact{Visible: true, Rule: access.Rule{Level: access.Public}}
		if actor.ID == "" {
			return f, true, nil
		}
		up, err := m.CanUpload(ctx, actor, media.UploadTarget{Ref: r})
		f.Editor = up.Allowed
		return f, true, err
	}
	return access.Fact{}, false, nil
}

// CanUpload: post media needs channel:posts:create (a draft: its author),
// channel images need channel:settings:manage, a user uploads only their own
// avatar. Site admins may upload to any post or channel and are exempt from
// the upload limits. The channel owns the quota of its posts.
func (m *mediaService) CanUpload(ctx context.Context, actor access.Actor, t media.UploadTarget) (media.UploadGrant, error) {
	user := actor.ID
	if actor.Anonymous || user == "" {
		return media.UploadGrant{}, nil
	}
	switch t.Ref.ContentKind {
	case kindPost:
		_, up, _, _, err := m.postAccess(ctx, actor, t.Ref.ContentID)
		return up, err
	case kindChannel:
		active, err := m.channels.active(ctx, t.Ref.ContentID)
		if err != nil || !active {
			return media.UploadGrant{}, err
		}
		g, err := m.grants(ctx, user, t.Ref.ContentID)
		admin := g.can(postEditPermission)
		return media.UploadGrant{Owner: channelOwner(t.Ref.ContentID), Exempt: admin, Allowed: admin || g.can(channelSettingsManage)}, err
	case kindUser:
		admin, err := m.auth.can(ctx, user, iam.RootGroup(), postEditPermission)
		return media.UploadGrant{Exempt: admin, Allowed: user == t.Ref.ContentID}, err
	}
	return media.UploadGrant{}, nil
}

type mediaActorKey struct{}

// api serves ContentKit's HTTP API under one prefix behind AuthKit's optional
// verification: uploads at /media/upload, reads at /media, post codes at
// /codes. Posts are the app's own, so the content module registers no kinds
// and signed-out visitors only read.
func (m *mediaService) api(ctx context.Context, cfg Config, urls *contenturl.Router) (http.Handler, error) {
	rt, err := contentkit.NewRuntime(ctx, contentkit.RuntimeConfig{
		EmbeddedConfig: contentkit.EmbeddedConfig{PG: m.pool, PGSchema: contentSchema(cfg), Tenant: tenant},
		Auth:           m.auth.client.Authenticator(), UserInfo: m.auth.client.UserInfo(),
		Content: content.Options{Resolver: m.resolver},
		Uploads: m.uploads, Reader: m.reader, Codes: urls,
	})
	if err != nil {
		return nil, err
	}
	return withMediaActor(http.StripPrefix(contentkitPrefix, rt.Handler())), nil
}

const contentkitPrefix = "/api/v1/contentkit"

// readiness is a post's media processing; a post without media is ready.
func (m *mediaService) readiness(ctx context.Context, id string) (*media.Readiness, error) {
	if m == nil {
		return &media.Readiness{State: media.StateReady}, nil
	}
	man, _, err := m.manifests.Get(ctx, postRef(id))
	if errors.Is(err, media.ErrNotFound) {
		return &media.Readiness{State: media.StateReady}, nil
	} else if err != nil {
		return nil, err
	}
	k, err := m.kinds.Kind(kindPost)
	if err != nil {
		return nil, err
	}
	r := k.Readiness(man)
	return &r, nil
}

// hasMedia reports whether a post holds an attached image or video.
func (m *mediaService) hasMedia(ctx context.Context, id string) (bool, error) {
	man, _, err := m.manifests.Get(ctx, postRef(id))
	if errors.Is(err, media.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return slices.ContainsFunc(man.Files, func(f media.File) bool { return f.IsUpload() && !f.Unattached }), nil
}

// Actor implements media.Identity.
func (m *mediaService) Actor(ctx context.Context) (access.Actor, bool) {
	a, ok := ctx.Value(mediaActorKey{}).(access.Actor)
	return a, ok && !a.Anonymous
}

func withMediaActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := access.Actor{Anonymous: true}
		if fc, ok := adaptor.LocalContextFromHTTPRequest(r); ok {
			if id, ok := verify.IdentityFromContext(fc); ok && id.SubjectKind == helpersauth.SubjectUser {
				a = actorFor(id.Subject)
			}
		}
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			a.IP = host
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mediaActorKey{}, a)))
	})
}

// exposeTx renders or removes posts' public previews after a change to
// whether anonymous viewers may see them (publish).
func (m *mediaService) exposeTx(ctx context.Context, tx pgx.Tx, ids ...string) error {
	if m == nil {
		return nil
	}
	refs := make([]contentref.ContentRef, len(ids))
	for i, id := range ids {
		refs[i] = postRef(id)
	}
	return m.jobs.ExposeTx(ctx, tx, refs...)
}

// deletePostsTx erases post folders in the caller's delete transaction and
// releases their channel quota.
func (m *mediaService) deletePostsTx(ctx context.Context, tx pgx.Tx, channel string, ids ...string) error {
	items := make([]media.Deletion, len(ids))
	for i, id := range ids {
		items[i] = media.Deletion{Ref: postRef(id), Owner: channelOwner(channel)}
	}
	return m.jobs.DeleteItemsTx(ctx, tx, items...)
}

func (m *mediaService) deleteChannelTx(ctx context.Context, tx pgx.Tx, channel string) error {
	return m.jobs.DeleteItemsTx(ctx, tx, media.Deletion{Ref: ref(kindChannel, channel), Owner: channelOwner(channel)})
}

// eraseUser removes a purged account's avatar folder.
func (m *mediaService) eraseUser(ctx context.Context, user string) error {
	return pgx.BeginFunc(ctx, m.pool, func(tx pgx.Tx) error {
		return m.jobs.DeleteItemsTx(ctx, tx, media.Deletion{Ref: ref(kindUser, user)})
	})
}

// postReady is Hooks.ItemReady, run in the app's River client after the media
// worker's jobs: a publishing post goes live once its media is ready, its
// preview exposed in the same transaction. A failed one stays publishing;
// its editors see which file failed and remove it.
func (m *mediaService) postReady(ctx context.Context, tx pgx.Tx, r contentref.ContentRef, rd media.Readiness) error {
	if r.ContentKind != kindPost || !rd.Ready() {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE `+m.posts.table+` SET state='published', published_at=NOW(), created_at=NOW() WHERE id=$1 AND state='publishing' AND deleted_at IS NULL`, r.ContentID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	slog.Info("post published after processing", "post", r.ContentID)
	return m.jobs.ExposeTx(ctx, tx, r)
}
