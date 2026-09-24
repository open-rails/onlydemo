package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/open-rails/authkit"
	"github.com/open-rails/authkit/verify"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/contentkit/media/image"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/contentkit/media/tiered"
	"github.com/open-rails/contentkit/media/token"
	"github.com/open-rails/contentkit/media/video"
	"github.com/open-rails/contentkit/migrations"
)

// Media kinds. Post folders hold ordered images and videos plus an optional
// blurred image teaser; channel and user folders hold only public slots.
const (
	kindPost    = "post"
	kindChannel = "channel"
)

// Post ceilings (teaser included in the file count).
const (
	maxImageBytes = 25 << 20
	maxVideoBytes = 20 << 30 // room for long 4K sources; multipart
	maxPostFiles  = 50
	maxPostVideos = 10
)

var (
	imageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}
	videoTypes = []string{"video/mp4", "video/webm", "video/quicktime", "video/x-matroska"}
	// editorSpec is the whole source, ignoring crop/rotate, for the cropper;
	// only editors (Resolution.Editor) are signed it.
	editorSpec = media.Spec{Width: 1200, Height: 1200, Fit: media.FitInside, Quality: 80, Unedited: true, EditorOnly: true}
	postSpecs  = map[string]media.Spec{
		"large":  {Width: 1600, Height: 1600, Fit: media.FitInside, Quality: 85},
		"thumb":  {Width: 480, Height: 480, Fit: media.FitCover, Quality: 80},
		"editor": editorSpec,
	}
	teaserSpecs = map[string]media.Spec{
		"blurred": {Width: 960, Height: 960, Fit: media.FitInside, Quality: 70, Blur: 24},
	}
	mediaKinds = []media.Kind{
		{Name: kindPost, Types: append(append([]string{}, imageTypes...), videoTypes...), MaxBytes: maxVideoBytes, MaxFiles: maxPostFiles,
			TypeLimits: map[string]media.Limit{"image": {MaxBytes: maxImageBytes}, "video": {MaxFiles: maxPostVideos}},
			Specs:      postSpecs, Video: &media.Video{}}, // default ladder (short sides up to 2160), aspects 1:2.4–2.4:1
		{Name: kindChannel, Types: imageTypes, MaxBytes: maxImageBytes, Slots: map[string]media.Slot{slotAvatar: avatarSlot, slotCover: coverSlot}},
		{Name: media.UserKind, Types: imageTypes, MaxBytes: maxImageBytes, Slots: map[string]media.Slot{slotAvatar: avatarSlot}},
	}
	policyLevels = map[string]tiered.Level{
		"public": tiered.Public, "membership": tiered.Members, "ppv": tiered.PPV, "members_ppv": tiered.MembersPPV,
	}
)

type mediaConfig struct {
	Tenant           string
	S3Endpoint       string
	S3PublicEndpoint string
	S3Bucket         string
	S3Region         string
	S3AccessKeyID    string
	S3SecretKey      string
	URL              string // media-access origin
	Delivery         media.DeliveryMode
	CookieDomain     string
	TokenKey         string
	TokenKeyPrevious string
	FilesPerHour     int
	BytesPerDay      int64
	ChannelQuota     int64
}

func loadMediaConfig(get func(string) string) (mediaConfig, error) {
	or := func(key, def string) string {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v
		}
		return def
	}
	c := mediaConfig{
		Tenant:           or("media_tenant", "o"),
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
	pool      *pgxpool.Pool
	slotTable string
	limiter   *media.PGLimiter
	auth      *appAuth
	billing   *billingService
	channels  *channelAPI
	posts     *postAPI
}

// applyContentMigrations installs ContentKit's baseline (the upload limiter's
// counters) in its own schema and the video queue (River schema
// media_worker, drained by cmd/media-worker).
func applyContentMigrations(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	if err := migrations.ApplyPostgres(ctx, db, contentSchema(cfg)); err != nil {
		return err
	}
	return video.Migrate(ctx, pool)
}

func newMedia(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, billing *billingService, channels *channelAPI, posts *postAPI) (*mediaService, error) {
	mc := cfg.Media
	kinds, err := media.NewRegistry(mediaKinds...)
	if err != nil {
		return nil, err
	}
	s3cfg := mediaS3.Config{Bucket: mc.S3Bucket, Region: mc.S3Region, Endpoint: mc.S3Endpoint, PublicEndpoint: mc.S3PublicEndpoint,
		AccessKeyID: mc.S3AccessKeyID, SecretAccessKey: mc.S3SecretKey, UsePathStyle: true}
	probe, err := mediaS3.New(s3cfg)
	if err != nil {
		return nil, err
	}
	if s3cfg.Capabilities, err = media.Probe(ctx, probe, "_probe/"); err != nil {
		return nil, fmt.Errorf("probe media bucket %s: %w", mc.S3Bucket, err)
	}
	store, err := mediaS3.New(s3cfg)
	if err != nil {
		return nil, err
	}
	ring, err := token.ParseRing(mc.TokenKey, mc.TokenKeyPrevious)
	if err != nil {
		return nil, fmt.Errorf("MEDIA_TOKEN_KEY: %w", err)
	}
	signing, _ := token.ParseKey(mc.TokenKey)
	m := &mediaService{cfg: mc, store: store, kinds: kinds, auth: auth, billing: billing, channels: channels, posts: posts,
		pool: pool, slotTable: pgx.Identifier{appSchema(cfg), "media_slots"}.Sanitize()}
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
	// Resolver publishes video posters and hover previews by what anonymous
	// viewers may see: drafts nothing, public posts both, paid posts the poster.
	if m.jobs, err = media.NewJobs(media.JobsConfig{Store: store, Kinds: kinds, Tenants: []string{mc.Tenant}, Limiter: m.limiter, Resolver: m}); err != nil {
		return nil, err
	}
	if m.manifests, err = media.NewManifests(store, kinds, media.ManifestOptions{Locker: media.PGLocker(pool), Jobs: m.jobs}); err != nil {
		return nil, err
	}
	hooks := media.Hooks{DownloadName: m.downloadName, Failed: func(_ context.Context, ref contentref.ContentRef, file string, err error) {
		slog.Warn("media file cannot be derived", "ref", ref.String(), "file", file, "err", err)
	}, SlotEncoded: m.slotEncoded}
	proc, err := image.New(image.Config{Store: store, Kinds: kinds, Manifests: m.manifests, Hooks: hooks,
		Specs: func(k media.Kind, f media.File) map[string]media.Spec {
			if k.Name == kindPost && f.Teaser() {
				return teaserSpecs
			}
			return k.Specs
		}})
	if err != nil {
		return nil, err
	}
	if err = m.jobs.AddProcessor(proc.Process); err != nil {
		return nil, err
	}
	videos, err := video.NewEnqueuer(pool, kinds)
	if err != nil {
		return nil, err
	}
	if err = m.jobs.AddProcessor(videos.Processor()); err != nil {
		return nil, err
	}
	uploadOptions := media.UploadOptions{Store: store, Kinds: kinds, Manifests: m.manifests,
		Authorizer: m, Tickets: &ring, Limiter: m.limiter, Queue: m.jobs}
	// The poster picker's exact frames need ffmpeg in the app; without it /frame answers not_found.
	if frames, err := video.NewFrames(store, ""); err == nil {
		uploadOptions.Frames = frames
	} else {
		slog.Warn("video poster frame grabs are unavailable", "err", err)
	}
	if m.uploads, err = media.NewUploads(uploadOptions); err != nil {
		return nil, err
	}
	if m.reader, err = media.NewReader(media.ReaderOptions{Manifests: m.manifests, Kinds: kinds, Resolver: m, Hooks: hooks,
		Progress: video.NewProgressSource(pool),
		Delivery: media.Delivery{Mode: mc.Delivery, BaseURL: mc.URL, CookieDomain: mc.CookieDomain, SigningKey: signing}}); err != nil {
		return nil, err
	}
	channels.media, posts.media, auth.media = m, m, m
	return m, nil
}

func (m *mediaService) ref(kind, id string) contentref.ContentRef {
	return contentref.New(m.cfg.Tenant, kind, id)
}

func (m *mediaService) postRef(id int64) contentref.ContentRef {
	return m.ref(kindPost, strconv.FormatInt(id, 10))
}

func channelOwner(id string) string { return kindChannel + ":" + id }

// postPolicy maps a post's access policy onto OpenRails entitlement keys. A
// purchase key grants every paid level, so buyers keep access after policy or
// membership changes; members_ppv checks only the purchase.
func postPolicy(p post) tiered.Policy {
	return tiered.Policy{Level: policyLevels[p.AccessPolicy], Membership: membershipResource(p.ChannelID), Purchase: postResource(p.BillingKey)}
}

func actorFor(user string) access.Actor {
	return access.Actor{ID: user, Kind: "user", Anonymous: user == ""}
}

// grants reads user's grants on a channel, root grants included, in one call.
func (m *mediaService) grants(ctx context.Context, user, channel string) (grants, error) {
	if user == "" {
		return nil, nil
	}
	perms, err := m.auth.client.EffectivePermissionsForGroups(ctx, authkit.UserSubject(user), []string{channel})
	if err != nil || len(perms[channel]) == 0 {
		return nil, err
	}
	if live, err := m.channels.accountLive(ctx, user); err != nil || !live {
		return nil, err
	}
	return perms[channel], nil
}

type grants []authkit.Perm

func (g grants) can(perm authkit.Perm) bool { return slices.ContainsFunc(g, perm.Matches) }

// postAccess loads a live post (found false otherwise) and the actor's upload
// grant and grants on its channel.
func (m *mediaService) postAccess(ctx context.Context, actor access.Actor, contentID string) (p post, up media.UploadGrant, g grants, found bool, err error) {
	id, err := strconv.ParseInt(contentID, 10, 64)
	if err != nil {
		return p, up, nil, false, nil
	}
	p, err = m.posts.visible(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, up, nil, false, nil
	} else if err != nil {
		return p, up, nil, false, err
	}
	if g, err = m.grants(ctx, actor.ID, p.ChannelID); err != nil {
		return p, up, nil, false, err
	}
	admin := g.can(postEditPermission)
	up = media.UploadGrant{Owner: channelOwner(p.ChannelID), Exempt: admin,
		Allowed: admin || (!p.Draft || p.AuthorID == actor.ID) && g.can(channelCreatePermission)}
	return p, up, g, true, nil
}

// Resolve answers ContentKit's batch port. Media resolves one ref per
// request (access.ResolveOne), so each ref takes the single-ref decision.
func (m *mediaService) Resolve(ctx context.Context, refs []contentref.ContentRef, actor access.Actor) (map[contentref.ContentKey]access.Resolution, error) {
	out := make(map[contentref.ContentKey]access.Resolution, len(refs))
	for _, ref := range refs {
		r, err := m.resolve(ctx, ref, actor)
		if err != nil {
			return nil, err
		}
		out[ref.Key()] = r
	}
	return out, nil
}

// resolve is the one read decision for media: the same rule as the post API.
// Channel editors and site admins read everything. Editors are whoever may
// upload to the item: they alone get the uncropped editor variant and the
// files' edits and source dims.
func (m *mediaService) resolve(ctx context.Context, ref contentref.ContentRef, actor access.Actor) (access.Resolution, error) {
	switch ref.ContentKind {
	case kindPost:
		p, up, g, found, err := m.postAccess(ctx, actor, ref.ContentID)
		if err != nil || !found {
			return access.Resolution{}, err
		}
		if p.Draft {
			// Only its author (or a site admin) sees a draft.
			return access.Resolution{Visible: up.Allowed, Accessible: up.Allowed, Editor: up.Allowed}, nil
		}
		ok := g.can(channelReadPermission) || g.can(postReadPermission)
		if !ok {
			ok, err = tiered.Decide(ctx, m.billing.checker(), actor, postPolicy(p))
		}
		return access.Resolution{Visible: true, Accessible: ok, Editor: up.Allowed}, err
	case kindChannel, media.UserKind:
		// Public avatars and covers.
		r := access.Resolution{Visible: true, Accessible: true}
		if actor.ID == "" {
			return r, nil
		}
		up, err := m.CanUpload(ctx, actor, ref)
		r.Editor = up.Allowed
		return r, err
	}
	return access.Resolution{}, nil
}

// CanUpload: post media needs channel:posts:create (a draft: its author), channel slots need
// channel:settings:manage, a user uploads only their own avatar. Site admins
// may upload to any post or channel and are exempt from the UploadLimiter. The channel owns
// the quota of its posts.
func (m *mediaService) CanUpload(ctx context.Context, actor access.Actor, ref contentref.ContentRef) (media.UploadGrant, error) {
	user := actor.ID
	if actor.Anonymous || user == "" {
		return media.UploadGrant{}, nil
	}
	switch ref.ContentKind {
	case kindPost:
		_, up, _, _, err := m.postAccess(ctx, actor, ref.ContentID)
		return up, err
	case kindChannel:
		active, err := m.channels.active(ctx, ref.ContentID)
		if err != nil || !active {
			return media.UploadGrant{}, err
		}
		g, err := m.grants(ctx, user, ref.ContentID)
		admin := g.can(postEditPermission)
		return media.UploadGrant{Owner: channelOwner(ref.ContentID), Exempt: admin, Allowed: admin || g.can("channel:settings:manage")}, err
	case media.UserKind:
		root, err := m.auth.client.ListEffectivePermissions(ctx, authkit.UserSubject(user), authkit.RootGroup())
		admin := err == nil && grants(root).can(postEditPermission)
		if admin {
			admin, err = m.channels.accountLive(ctx, user)
		}
		return media.UploadGrant{Exempt: admin, Allowed: user == ref.ContentID}, err
	}
	return media.UploadGrant{}, nil
}

type mediaActorKey struct{}

// mount serves the upload API (the browser SDK) and the read API behind
// AuthKit's optional verification.
func (m *mediaService) mount(app fiber.Router, optional fiber.Handler) {
	upload := media.UploadHandler(m.uploads, media.UploadHandlerOptions{Tenant: m.cfg.Tenant, Reader: m.reader,
		Actor: func(r *http.Request) (access.Actor, bool) {
			a, _ := r.Context().Value(mediaActorKey{}).(access.Actor)
			return a, !a.Anonymous
		}})
	read := m.reader.Handler(media.HandlerOptions{Tenant: m.cfg.Tenant, Identity: m})
	uploadAPI := adaptor.HTTPHandlerWithContext(withMediaActor(http.StripPrefix("/api/v1/media/upload", m.imageTeasers(upload))))
	app.Post("/api/v1/media/upload/*", optional, uploadAPI)
	app.Get("/api/v1/media/upload/frame", optional, uploadAPI)
	app.Get("/api/v1/posts/:id/media", optional, m.files)
	app.Get("/api/v1/media/*", optional, adaptor.HTTPHandlerWithContext(withMediaActor(http.StripPrefix("/api/v1/media", read))))
}

// files lists a post's manifest with original names for its uploaders, who
// reorder, remove and pick the teaser by name; readers use the read API.
func (m *mediaService) files(c fiber.Ctx) error {
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	ref := m.postRef(id)
	g, err := m.CanUpload(c.Context(), actorFor(viewer(c)), ref)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	if !g.Allowed {
		return clientError(c, 404, "post not found")
	}
	man, _, err := m.manifests.Get(c.Context(), ref)
	if errors.Is(err, media.ErrNotFound) {
		man = &media.Manifest{}
	} else if err != nil {
		return clientError(c, 500, "media store error")
	}
	out := media.CommitReply{Files: make([]media.CommitFile, len(man.Files))}
	for i, f := range man.Files {
		out.Files[i] = media.CommitFile{Name: f.Name, Original: f.Original, Type: f.Type, Size: f.Size, Edit: f.Edit, Meta: f.Meta}
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(out)
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
			if cl, ok := verify.UserClaimsFromContext(fc); ok {
				a = actorFor(cl.UserID)
			}
		}
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			a.IP = host
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mediaActorKey{}, a)))
	})
}

// publishTx republishes posts' public video images after a change to what
// anonymous viewers may see (publish, access policy).
func (m *mediaService) publishTx(ctx context.Context, tx pgx.Tx, ids ...int64) error {
	if m == nil {
		return nil
	}
	refs := make([]contentref.ContentRef, len(ids))
	for i, id := range ids {
		refs[i] = m.postRef(id)
	}
	return m.jobs.PublishTx(ctx, tx, refs...)
}

// deletePostsTx erases post folders in the caller's delete transaction and
// releases their channel quota.
func (m *mediaService) deletePostsTx(ctx context.Context, tx pgx.Tx, channel string, ids ...int64) error {
	items := make([]media.Deletion, len(ids))
	for i, id := range ids {
		items[i] = media.Deletion{Ref: m.postRef(id), Owner: channelOwner(channel)}
		if err := m.deleteSlotsTx(ctx, tx, kindPost, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
	}
	return m.jobs.DeleteItemsTx(ctx, tx, items...)
}

func (m *mediaService) deleteChannelTx(ctx context.Context, tx pgx.Tx, channel string) error {
	if err := m.deleteSlotsTx(ctx, tx, kindChannel, channel); err != nil {
		return err
	}
	return m.jobs.DeleteItemsTx(ctx, tx, media.Deletion{Ref: m.ref(kindChannel, channel), Owner: channelOwner(channel)})
}

// eraseUser removes a purged account's avatar folder.
func (m *mediaService) eraseUser(ctx context.Context, user string) error {
	return pgx.BeginFunc(ctx, m.posts.pool, func(tx pgx.Tx) error {
		if err := m.deleteSlotsTx(ctx, tx, media.UserKind, user); err != nil {
			return err
		}
		return m.jobs.EraseUserTx(ctx, tx, m.cfg.Tenant, user)
	})
}

var qualityKey = regexp.MustCompile(`^(.+)-(\d+p)$`)

// downloadName saves a post's video download as "{post-slug}-{file}-{rung}p.mp4"
// (video.DownloadKey: rungs are short sides, so a vertical 1080p is 1080 wide).
func (m *mediaService) downloadName(ctx context.Context, ref contentref.ContentRef, key string, d media.Download) (string, error) {
	id, err := strconv.ParseInt(ref.ContentID, 10, 64)
	if err != nil || ref.ContentKind != kindPost {
		return ref.ContentID + "-" + key, nil
	}
	p, err := m.posts.visible(ctx, id)
	if err != nil {
		return "", err
	}
	q := qualityKey.FindStringSubmatch(key)
	if q == nil || d.Type != "video/mp4" {
		return p.Slug + "-" + key, nil
	}
	return p.Slug + "-" + strings.TrimSuffix(q[1], path.Ext(q[1])) + "-" + q[2] + ".mp4", nil
}
