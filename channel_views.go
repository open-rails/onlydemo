package main

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/open-rails/authkit"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/openrails"
)

var channelSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)

type channelView struct {
	ID         string              `json:"id"`
	Slug       string              `json:"slug"`
	Name       string              `json:"name"`
	Role       string              `json:"role,omitempty"`
	CanManage  bool                `json:"can_manage"`
	CanEdit    bool                `json:"can_edit"`
	Avatar     *media.SlotManifest `json:"avatar"`
	Cover      *media.SlotManifest `json:"cover"`
	Membership channelMembership   `json:"membership"`
}

// view renders one live channel for the viewer, with their free grant.
func (api *channelAPI) view(c fiber.Ctx, id string) (channelView, error) {
	views, err := api.views(c.Context(), viewer(c), []string{id}, nil, true)
	if err != nil {
		return channelView{}, err
	}
	if len(views) == 0 {
		return channelView{}, authkit.ErrGroupNotFound
	}
	return views[0], nil
}

// views renders the live channels among ids, in order, from one page load;
// known carries groups the caller already read. grants reports free
// memberships, which costs one entitlement listing.
func (api *channelAPI) views(ctx context.Context, user string, ids []string, known map[string]authkit.GroupInstance, grants bool) ([]channelView, error) {
	keys := []string{}
	if user != "" {
		for _, id := range ids {
			keys = append(keys, membershipResource(id))
		}
	}
	pg, err := api.loadPage(ctx, user, ids, known, keys, slotAvatar, slotCover)
	if err != nil {
		return nil, err
	}
	out, selling := []channelView{}, []string{}
	for _, id := range unique(ids) {
		if !pg.live(id) {
			continue
		}
		g, s, key := pg.groups[id], pg.states[id], membershipResource(id)
		v := channelView{ID: id, Slug: g.InstanceSlug, Name: g.DisplayName, CanManage: pg.can(id, "channel:settings:manage"), CanEdit: pg.can(id, channelEditPermission),
			Avatar: pg.slots[slotKey{id, slotAvatar}], Cover: pg.slots[slotKey{id, slotCover}],
			Membership: channelMembership{Status: s.Status, Free: s.Free, Sync: s.Sync, Member: pg.access[key]}}
		if v.CanManage {
			v.Role = "owner"
		} else if v.CanEdit {
			v.Role = "editor"
		}
		// The price shows wherever the channel does, so joining is one click.
		if s.Status == membershipOpen && !s.Free {
			selling = append(selling, key)
		}
		out = append(out, v)
	}
	offers, err := api.billing.offers(ctx, openrails.OfferRecurring, selling, 1)
	if err != nil {
		return nil, err
	}
	var held []openrails.EntitlementRecord
	if grants && user != "" && len(out) > 0 {
		if held, err = api.billing.reads.ListEntitlements(ctx, user, time.Time{}); err != nil {
			return nil, err
		}
	}
	for i := range out {
		v := &out[i]
		if list := offers[membershipResource(v.ID)]; len(list) > 0 {
			v.Membership.Offer = &list[0]
		}
		v.Membership.FreeMember = v.Membership.Member && len(freeGrants(held, v.ID)) > 0
	}
	return out, nil
}

func (api *channelAPI) list(c fiber.Ctx) error {
	cursor := c.Query("cursor")
	if cursor != "" {
		if _, err := channelID(cursor); err != nil {
			return clientError(c, 400, "invalid cursor")
		}
	}
	rows, err := api.pool.Query(c.Context(), `SELECT id::text FROM `+api.table+` WHERE deleted_at IS NULL AND ($1::text='' OR id::text>$1) ORDER BY id LIMIT 26`, cursor)
	if err != nil {
		return databaseError(c, err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return databaseError(c, err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return databaseError(c, err)
	}
	more := len(ids) > 25
	if more {
		ids = ids[:25]
	}
	data, err := api.views(c.Context(), viewer(c), ids, nil, false)
	if err != nil {
		return billingUnavailable(c)
	}
	next := ""
	if more {
		next = ids[len(ids)-1]
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"data": data, "has_more": more, "next_cursor": next})
}

// resolve addresses a channel by its AuthKit group slug (or former-name alias) or its id.
func (api *channelAPI) resolve(ctx context.Context, raw string) (string, error) {
	if id, err := channelID(raw); err == nil {
		return id, nil
	}
	slug := strings.ToLower(raw)
	if !channelSlug.MatchString(slug) {
		return "", authkit.ErrGroupNotFound
	}
	group, err := api.auth.client.GroupInstanceForSlug(ctx, authkit.GroupRef{Persona: channelPersona, Instance: slug})
	if err != nil {
		return "", err
	}
	return group.ID, nil
}
func (api *channelAPI) publicGet(c fiber.Ctx) error {
	id, err := api.resolve(c.Context(), c.Params("id"))
	if errors.Is(err, authkit.ErrGroupNotFound) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	v, err := api.view(c, id)
	if errors.Is(err, authkit.ErrGroupNotFound) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(v)
}
func (api *channelAPI) members(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
	}
	allowed, err := api.allowed(c.Context(), viewer(c), id, "channel:settings:manage")
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed {
		return clientError(c, 404, "channel not found")
	}
	g, err := api.auth.client.GroupInstanceByID(c.Context(), id)
	if err != nil {
		return clientError(c, 404, "channel not found")
	}
	ref := authkit.GroupRef{Persona: channelPersona, Instance: g.InstanceSlug}
	if c.Method() == "GET" {
		members, e := api.auth.client.ListGroupMembers(c.Context(), ref)
		if e != nil {
			return billingUnavailable(c)
		}
		data := []fiber.Map{}
		for _, m := range members {
			data = append(data, fiber.Map{"user_id": m.SubjectID, "subject_kind": m.SubjectKind, "role": m.Role})
		}
		return c.JSON(fiber.Map{"data": data})
	}
	if c.Method() == "DELETE" {
		members, e := api.auth.client.ListGroupMembers(c.Context(), ref)
		if e != nil {
			return billingUnavailable(c)
		}
		for _, m := range members {
			if m.SubjectID == c.Params("user_id") && m.SubjectKind == authkit.SubjectKindUser {
				if e = api.auth.client.UnassignGroupRoleAs(c.Context(), viewer(c), ref, authkit.UserSubject(m.SubjectID), m.Role); e != nil {
					return clientError(c, 409, "member cannot be removed; an active owner is required")
				}
			}
		}
		return c.SendStatus(204)
	}
	var in struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.Role != "owner" && in.Role != "editor" {
		return clientError(c, 400, "role must be owner or editor")
	}
	user, err := api.auth.client.GetUserByUsername(c.Context(), strings.TrimSpace(in.Username))
	if err != nil || user == nil {
		return clientError(c, 404, "user not found")
	}
	if err = api.auth.client.AssignGroupRoleAs(c.Context(), viewer(c), ref, authkit.UserSubject(user.ID), authkit.Role(in.Role)); err != nil {
		return clientError(c, 403, "member could not be assigned")
	}
	return c.Status(201).JSON(fiber.Map{"user_id": user.ID, "role": in.Role})
}
func (api *postAPI) me(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	user := viewer(c)
	profile, err := api.auth.client.AdminGetUser(c.Context(), user)
	if err != nil {
		return billingUnavailable(c)
	}
	managed, err := api.channels.editable(c, user)
	if err != nil {
		return billingUnavailable(c)
	}
	before := int64(0)
	if raw := c.Query("before"); raw != "" {
		before, _ = strconv.ParseInt(raw, 10, 64)
	}
	// The library is every post the viewer holds a purchase of.
	held, err := api.billing.reads.ListEntitlements(c.Context(), user, time.Time{})
	if err != nil {
		return billingUnavailable(c)
	}
	keys := []string{}
	for _, e := range held {
		if key, ok := strings.CutPrefix(e.Entitlement, postResource("")); ok && e.RevokedAt == nil && uuid.Validate(key) == nil {
			keys = append(keys, key)
		}
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE billing_key=ANY($2::uuid[]) AND deleted_at IS NULL`+published+` AND ($1::bigint=0 OR id<$1) AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL) ORDER BY id DESC LIMIT 51`, before, keys)
	if err != nil {
		return databaseError(c, err)
	}
	purchased, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (post, error) { return scanPost(row) })
	if err != nil {
		return databaseError(c, err)
	}
	more := len(purchased) > 50
	next := ""
	if more {
		purchased = purchased[:50]
		next = strconv.FormatInt(purchased[len(purchased)-1].ID, 10)
	}
	if err = api.decorate(c, purchased, false); err != nil {
		return billingUnavailable(c)
	}
	subscriptions, err := api.billing.client.ListSubscriptions(c.Context(), openrails.SubscriptionFilter{CustomerID: user, PageOptions: openrails.PageOptions{Limit: 50}})
	if err != nil {
		return billingUnavailable(c)
	}
	payments, err := api.billing.client.ListPayments(c.Context(), openrails.PaymentFilter{CustomerID: user, PageOptions: openrails.PageOptions{Limit: 50}})
	if err != nil {
		return billingUnavailable(c)
	}
	avatar, err := api.media.slots(c.Context(), media.UserKind, []string{profile.ID}, slotAvatar)
	if err != nil {
		return databaseError(c, err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"user": fiber.Map{"id": profile.ID, "username": profile.Username, "email": profile.Email, "avatar": avatar[slotKey{profile.ID, slotAvatar}]}, "manageable_channels": managed, "purchased_posts": purchased, "has_more": more, "next_cursor": next, "subscriptions": subscriptions.Data, "payments": payments.Data})
}

// editable lists the channels the user can publish to.
func (api *channelAPI) editable(c fiber.Ctx, user string) ([]channelView, error) {
	groups, err := api.auth.client.ListSubjectGroups(c.Context(), authkit.UserSubject(user))
	if err != nil {
		return nil, err
	}
	ids, known := []string{}, map[string]authkit.GroupInstance{}
	for _, g := range groups {
		if g.Persona == channelPersona {
			ids = append(ids, g.GroupID)
			known[g.GroupID] = authkit.GroupInstance{ID: g.GroupID, Persona: g.Persona, InstanceSlug: g.InstanceSlug, DisplayName: g.DisplayName}
		}
	}
	views, err := api.views(c.Context(), user, ids, known, false)
	if err != nil {
		return nil, err
	}
	out := []channelView{}
	for _, v := range views {
		if v.CanEdit {
			out = append(out, v)
		}
	}
	return out, nil
}

func (api *channelAPI) mine(c fiber.Ctx) error {
	list, err := api.editable(c, viewer(c))
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"data": list})
}
