package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	helpersauth "github.com/open-rails/authkit/auth"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/authkit/verify"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
)

// postAPI serves the app's posts until ContentKit's catalog stores them
// (docs/contentkit-cutover.md): each sits in one ContentKit channel group,
// whose AuthKit scope says who edits it, and opens by ContentKit's keys.
type postAPI struct {
	pool  *pgxpool.Pool
	auth  *appAuth
	gate  *access.Gate
	ck    *ckAPI
	table string
}

func newPosts(pool *pgxpool.Pool, auth *appAuth, gate *access.Gate, cfg Config) *postAPI {
	return &postAPI{pool: pool, auth: auth, gate: gate, table: pgx.Identifier{appSchema(cfg), "posts"}.Sanitize()}
}

type post struct {
	ID           string     `json:"id"`
	AuthorID     string     `json:"author_id"`
	ChannelID    string     `json:"channel_id"`
	ChannelSlug  string     `json:"channel_slug"`
	ChannelName  string     `json:"channel_name"`
	Title        string     `json:"title"`
	Body         string     `json:"body,omitempty"`
	AccessPolicy string     `json:"access_policy"`
	State        string     `json:"state"`
	Deleted      bool       `json:"-"`
	CanRead      bool       `json:"can_read"`
	CanEdit      bool       `json:"can_edit"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type postInput struct {
	ChannelID    *string `json:"channel_id"`
	Title        *string `json:"title"`
	Body         *string `json:"body"`
	AccessPolicy *string `json:"access_policy"`
	// Draft creates an empty draft (the composer's media folder); DraftID
	// publishes that draft.
	Draft   bool    `json:"draft"`
	DraftID *string `json:"draft_id"`
}

const postColumns = `id::text,author_id::text,channel_id::text,title,body,access_policy,state,deleted_at IS NOT NULL,published_at,created_at,updated_at`

const (
	stateDraft     = "draft"
	statePublished = "published"
)

func scanPost(row interface{ Scan(...any) error }) (post, error) {
	var p post
	err := row.Scan(&p.ID, &p.AuthorID, &p.ChannelID, &p.Title, &p.Body, &p.AccessPolicy, &p.State, &p.Deleted, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// rule is the post's access rule: channel-scoped levels name its group.
func (p post) rule() access.Rule {
	switch p.AccessPolicy {
	case "membership":
		return access.Rule{Level: access.GroupLevel, Group: p.ChannelID}
	case "members_ppv":
		return access.Rule{Level: access.GroupPPV, Group: p.ChannelID}
	case "ppv":
		return access.Rule{Level: access.PPV}
	}
	return access.Rule{Level: access.Public}
}

var errNoPost = errors.New("post not found")

// parsePostID parses a post id: a canonical UUIDv7.
func parsePostID(raw string) (string, error) {
	if contentref.ValidateID(raw) != nil {
		return "", errNoPost
	}
	return raw, nil
}

// viewer is the signed-in user AuthKit's gate verified, or "".
func viewer(c fiber.Ctx) string {
	if id, ok := verify.IdentityFromContext(c.Context()); ok && id.SubjectKind == helpersauth.SubjectUser {
		return id.Subject
	}
	return ""
}

func actorFor(user string) access.Actor {
	return access.Actor{ID: user, Kind: helpersauth.SubjectUser, Anonymous: user == ""}
}

type grants []iam.Perm

func (g grants) can(perm iam.Perm) bool { return slices.ContainsFunc(g, perm.Matches) }

// postAccess is what one viewer may do with each of a page's posts.
type postAccess struct {
	read, edit, upload, remove bool
}

// access decides posts for user in a fixed number of calls: one channel read
// per channel, one AuthKit read and one billing read. Posts whose channel is
// not an active ContentKit channel are absent.
func (api *postAPI) access(ctx context.Context, user string, posts []post) (map[string]postAccess, map[string]group, error) {
	ids := make([]string, len(posts))
	for i, p := range posts {
		ids[i] = p.ChannelID
	}
	channels, err := api.ck.channels(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	perms, err := api.auth.effective(ctx, user, slices.Collect(maps.Keys(channels)))
	if err != nil {
		return nil, nil, err
	}
	items := make([]access.Item, len(posts))
	for i, p := range posts {
		items[i] = access.Item{Content: p.ID, Rule: p.rule()}
		if p.Deleted {
			items[i].Rule = access.Rule{Level: access.PPV}
		}
	}
	decisions, err := api.gate.Decide(ctx, actorFor(user), items)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]postAccess{}
	for i, p := range posts {
		if _, ok := channels[p.ChannelID]; !ok {
			continue
		}
		g := grants(perms[p.ChannelID])
		admin := g.can(postEditPermission)
		a := postAccess{upload: admin || g.can(channelCreatePermission) && (p.State != stateDraft || p.AuthorID == user)}
		switch {
		case p.Deleted:
			// Only its buyers still read a deleted post.
			a.read = decisions[i].Allowed
		case p.State == stateDraft:
			a.read, a.edit, a.remove = a.upload, a.upload, a.upload
		default:
			a.read = decisions[i].Allowed || g.can(channelReadPermission) || g.can(postReadPermission)
			a.edit = admin || g.can(channelEditPermission)
			a.remove = g.can(postDeletePermission) || g.can(channelRemovePermission)
		}
		out[p.ID] = a
	}
	return out, channels, nil
}

// decorate fills posts' channel and the viewer's access, dropping posts the
// viewer may not see.
func (api *postAPI) decorate(ctx context.Context, user string, posts []post) ([]post, error) {
	acc, channels, err := api.access(ctx, user, posts)
	if err != nil {
		return nil, err
	}
	out := []post{}
	for _, p := range posts {
		a, ok := acc[p.ID]
		if !ok || p.Deleted && !a.read || p.State == stateDraft && !a.read {
			continue
		}
		g := channels[p.ChannelID]
		p.ChannelSlug, p.ChannelName, p.CanRead, p.CanEdit = g.Slug, g.Name, a.read, a.edit
		if !p.CanRead {
			p.Body = ""
		}
		out = append(out, p)
	}
	return out, nil
}

// list is the feed, global or one channel's, newest first: locked posts are
// listed with their body withheld.
func (api *postAPI) list(c fiber.Ctx) error {
	limit := 25
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			return clientError(c, 400, "limit must be between 1 and 50")
		}
		limit = n
	}
	before := c.Query("before")
	if before != "" {
		if _, err := parsePostID(before); err != nil {
			return clientError(c, 400, "invalid cursor")
		}
	}
	channel := c.Query("channel_id")
	if channel != "" {
		if _, err := parsePostID(channel); err != nil {
			return clientError(c, 400, "invalid channel_id")
		}
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE deleted_at IS NULL AND state='published' AND ($1::text='' OR channel_id::text=$1) AND ($2::text='' OR id<$2::uuid) ORDER BY id DESC LIMIT $3`, channel, before, limit+1)
	if err != nil {
		return databaseError(c, err)
	}
	posts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (post, error) { return scanPost(r) })
	if err != nil {
		return databaseError(c, err)
	}
	if len(posts) > limit {
		posts = posts[:limit]
		c.Set("X-Next-Cursor", posts[len(posts)-1].ID)
	}
	if posts, err = api.decorate(c.Context(), viewer(c), posts); err != nil {
		return unavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts)
}

// get serves a post: a draft to its editors, a deleted one to its buyers.
func (api *postAPI) get(c fiber.Ctx) error {
	id, err := parsePostID(c.Params("id"))
	if err != nil {
		return readError(c, err)
	}
	p, err := scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if err != nil {
		return readError(c, err)
	}
	posts, err := api.decorate(c.Context(), viewer(c), []post{p})
	if err != nil {
		return unavailable(c)
	}
	if len(posts) == 0 {
		return clientError(c, 404, "post not found")
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts[0])
}

func validatePost(p post) error {
	if len(p.Title) > 300 || len(p.Body) > 1_000_000 {
		return errors.New("post is too large")
	}
	switch p.AccessPolicy {
	case "public", "membership", "members_ppv", "ppv":
		return nil
	}
	return errors.New("invalid access_policy")
}

// channelGrants are user's grants on an active channel, root grants included.
func (api *postAPI) channelGrants(ctx context.Context, user, channel string) (grants, bool, error) {
	live, err := api.ck.channels(ctx, []string{channel})
	if err != nil || len(live) == 0 {
		return nil, false, err
	}
	perms, err := api.auth.effective(ctx, user, []string{channel})
	return perms[channel], true, err
}

// create publishes a post. With draft it instead makes an empty draft, whose
// id is the media folder the composer uploads to; draft_id publishes it.
func (api *postAPI) create(c fiber.Ctx) error {
	user := viewer(c)
	if user == "" {
		return clientError(c, 401, "a user access token is required")
	}
	var in postInput
	if err := bindJSON(c, &in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.ChannelID == nil {
		return clientError(c, 400, "channel_id is required")
	}
	channel, err := parsePostID(*in.ChannelID)
	if err != nil {
		return clientError(c, 400, "invalid channel_id")
	}
	g, live, err := api.channelGrants(c.Context(), user, channel)
	if err != nil {
		return unavailable(c)
	}
	if !live || !g.can(channelCreatePermission) && !g.can(postEditPermission) {
		return clientError(c, 404, "channel not found")
	}
	if in.Draft {
		var id string
		err := api.pool.QueryRow(c.Context(), `INSERT INTO `+api.table+`(author_id,channel_id,title,body) VALUES($1,$2,'','') RETURNING id::text`, user, channel).Scan(&id)
		if err != nil {
			return databaseError(c, err)
		}
		return c.Status(201).JSON(fiber.Map{"id": id, "channel_id": channel, "draft": true})
	}
	p := post{Title: deref(in.Title), Body: deref(in.Body), AccessPolicy: "public"}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	if err = validatePost(p); err != nil {
		return clientError(c, 400, err.Error())
	}
	var row pgx.Row
	if in.DraftID != nil {
		draft, err := parsePostID(*in.DraftID)
		if err != nil {
			return clientError(c, 404, "draft not found")
		}
		row = api.pool.QueryRow(c.Context(), `UPDATE `+api.table+` SET title=$1,body=$2,access_policy=$3,state='published',published_at=NOW(),created_at=NOW(),updated_at=NOW() WHERE id=$4 AND channel_id=$5 AND author_id=$6 AND state='draft' AND deleted_at IS NULL RETURNING `+postColumns, p.Title, p.Body, p.AccessPolicy, draft, channel, user)
	} else {
		row = api.pool.QueryRow(c.Context(), `INSERT INTO `+api.table+`(author_id,channel_id,title,body,access_policy,state,published_at) VALUES($1,$2,$3,$4,$5,'published',NOW()) RETURNING `+postColumns, user, channel, p.Title, p.Body, p.AccessPolicy)
	}
	if p, err = scanPost(row); errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "draft not found")
	} else if err != nil {
		return databaseError(c, err)
	}
	return api.reply(c, 201, p)
}

func (api *postAPI) reply(c fiber.Ctx, status int, p post) error {
	posts, err := api.decorate(c.Context(), viewer(c), []post{p})
	if err != nil || len(posts) == 0 {
		return unavailable(c)
	}
	return c.Status(status).JSON(posts[0])
}

// load reads a live post with the viewer's access to it.
func (api *postAPI) load(c fiber.Ctx) (post, postAccess, error) {
	id, err := parsePostID(c.Params("id"))
	if err != nil {
		return post{}, postAccess{}, err
	}
	p, err := scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL`, id))
	if err != nil {
		return p, postAccess{}, err
	}
	acc, _, err := api.access(c.Context(), viewer(c), []post{p})
	if err != nil {
		return p, postAccess{}, err
	}
	a, ok := acc[p.ID]
	if !ok {
		return p, a, errNoPost
	}
	return p, a, nil
}

func (api *postAPI) update(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	var in postInput
	if err := bindJSON(c, &in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.ChannelID != nil {
		return clientError(c, 400, "channel_id is immutable")
	}
	p, a, err := api.load(c)
	if err != nil {
		return readError(c, err)
	}
	if !a.edit || p.State == stateDraft {
		return clientError(c, 404, "post not found")
	}
	if in.Title != nil {
		p.Title = *in.Title
	}
	if in.Body != nil {
		p.Body = *in.Body
	}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	if err = validatePost(p); err != nil {
		return clientError(c, 400, err.Error())
	}
	p, err = scanPost(api.pool.QueryRow(c.Context(), `UPDATE `+api.table+` SET title=$1,body=$2,access_policy=$3,updated_at=NOW() WHERE id=$4 AND updated_at=$5 AND deleted_at IS NULL RETURNING `+postColumns, p.Title, p.Body, p.AccessPolicy, p.ID, p.UpdatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 409, "post changed; reload and retry")
	}
	if err != nil {
		return databaseError(c, err)
	}
	return api.reply(c, 200, p)
}

// delete removes a draft, or marks a post deleted: its buyers keep reading it.
func (api *postAPI) delete(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	p, a, err := api.load(c)
	if err != nil {
		return readError(c, err)
	}
	if !a.remove {
		return clientError(c, 404, "post not found")
	}
	if p.State == stateDraft {
		_, err = api.pool.Exec(c.Context(), `DELETE FROM `+api.table+` WHERE id=$1 AND state='draft'`, p.ID)
	} else {
		_, err = api.pool.Exec(c.Context(), `UPDATE `+api.table+` SET deleted_at=NOW() WHERE id=$1 AND deleted_at IS NULL`, p.ID)
	}
	if err != nil {
		return databaseError(c, err)
	}
	return c.SendStatus(204)
}

// postMedia answers ContentKit for posts' media folders until ContentKit
// stores posts: who sees, opens and uploads to each.
type postMedia struct{ api *postAPI }

func (m postMedia) decide(ctx context.Context, actor access.Actor, refs []contentref.ContentRef) (map[string]post, map[string]postAccess, error) {
	var ids []string
	for _, r := range refs {
		if r.ContentKind == kindPost && r.Version() == "" && contentref.ValidateID(r.ContentID) == nil {
			ids = append(ids, r.ContentID)
		}
	}
	posts := map[string]post{}
	if len(ids) == 0 {
		return posts, nil, nil
	}
	rows, err := m.api.pool.Query(ctx, `SELECT `+postColumns+` FROM `+m.api.table+` WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (post, error) { return scanPost(r) })
	if err != nil {
		return nil, nil, err
	}
	for _, p := range list {
		posts[p.ID] = p
	}
	user := ""
	if actor.Person() {
		user = actor.ID
	}
	acc, _, err := m.api.access(ctx, user, list)
	return posts, acc, err
}

func (m postMedia) Resolve(ctx context.Context, refs []contentref.ContentRef, actor access.Actor) (map[contentref.ContentKey]access.Resolution, error) {
	posts, acc, err := m.decide(ctx, actor, refs)
	if err != nil {
		return nil, err
	}
	out := map[contentref.ContentKey]access.Resolution{}
	for _, r := range refs {
		p, ok := posts[r.ContentID]
		a, live := acc[r.ContentID]
		if !ok || !live || r.ContentKind != kindPost {
			continue
		}
		res := access.Resolution{Visible: a.read || p.State == statePublished && !p.Deleted, Accessible: a.read, Editor: a.upload && !p.Deleted, Owner: p.AuthorID}
		if res.Visible {
			out[r.Key()] = res
		}
	}
	return out, nil
}

func (m postMedia) CanUpload(ctx context.Context, actor access.Actor, t media.UploadTarget) (media.UploadGrant, error) {
	if !actor.Person() {
		return media.UploadGrant{}, nil
	}
	posts, acc, err := m.decide(ctx, actor, []contentref.ContentRef{t.Ref})
	p, ok := posts[t.Ref.ContentID]
	if err != nil || !ok || p.Deleted || !acc[p.ID].upload {
		return media.UploadGrant{}, err
	}
	return media.UploadGrant{Allowed: true, Owner: kindChannel + ":" + p.ChannelID}, nil
}

func readError(c fiber.Ctx, err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNoPost) {
		return clientError(c, 404, "post not found")
	}
	return databaseError(c, err)
}

func clientError(c fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": message})
}
func databaseError(c fiber.Ctx, err error) error { return clientError(c, 500, "database error") }
func unavailable(c fiber.Ctx) error {
	return clientError(c, http.StatusServiceUnavailable, "a dependency is unavailable; retry")
}

// bindJSON refuses unknown fields and trailing values.
func bindJSON(c fiber.Ctx, value any) error {
	if len(c.Body()) > 1<<20 {
		return errors.New("request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
