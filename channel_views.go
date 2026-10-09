package main

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/openrails/billing"
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

var errNoChannel = errors.New("channel not found")

// view renders one live channel for the viewer, with their free grant.
func (api *channelAPI) view(c fiber.Ctx, id string) (channelView, error) {
	views, err := api.views(c.Context(), viewer(c), []string{id}, true)
	if err != nil {
		return channelView{}, err
	}
	if len(views) == 0 {
		return channelView{}, errNoChannel
	}
	return views[0], nil
}

// views renders the live channels among ids, in order, from one page load.
// grants reports free memberships, which costs one product-access check.
func (api *channelAPI) views(ctx context.Context, user string, ids []string, grants bool) ([]channelView, error) {
	keys := []string{}
	if user != "" {
		for _, id := range ids {
			keys = append(keys, membershipResource(id))
		}
	}
	pg, err := api.loadPage(ctx, user, ids, keys, slotAvatar, slotCover)
	if err != nil {
		return nil, err
	}
	out, selling := []channelView{}, []string{}
	for _, id := range unique(ids) {
		if !pg.live(id) {
			continue
		}
		g, s, key := pg.channels[id], pg.states[id], membershipResource(id)
		v := channelView{ID: id, Slug: g.Slug, Name: g.Name, CanManage: pg.can(id, channelSettingsManage), CanEdit: pg.can(id, channelEditPermission),
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
	offers, err := api.billing.offers(ctx, billing.OfferRecurring, selling, 1)
	if err != nil {
		return nil, err
	}
	free := map[string]bool{}
	if grants && user != "" && len(out) > 0 {
		products := []string{}
		for _, v := range out {
			if v.Membership.Member {
				products = append(products, freeMembershipProduct(v.ID))
			}
		}
		if len(products) > 0 {
			customer, err := customerID(user)
			if err != nil {
				return nil, err
			}
			if free, err = api.billing.reads.CheckProductAccess(ctx, customer, billing.CheckProductAccessParams{ProductKeys: products}); err != nil {
				return nil, err
			}
		}
	}
	for i := range out {
		v := &out[i]
		if list := offers[membershipResource(v.ID)]; len(list) > 0 {
			v.Membership.Offer = &list[0]
		}
		v.Membership.FreeMember = v.Membership.Member && free[freeMembershipProduct(v.ID)]
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
	data, err := api.views(c.Context(), viewer(c), ids, false)
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

// resolve addresses a live channel by its slug or its id.
func (api *channelAPI) resolve(ctx context.Context, raw string) (string, error) {
	if id, err := channelID(raw); err == nil {
		return id, nil
	}
	slug := strings.ToLower(raw)
	if !channelSlug.MatchString(slug) {
		return "", errNoChannel
	}
	var id string
	err := api.pool.QueryRow(ctx, `SELECT id::text FROM `+api.table+` WHERE slug=$1 AND deleted_at IS NULL`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNoChannel
	}
	return id, err
}
func (api *channelAPI) publicGet(c fiber.Ctx) error {
	id, err := api.resolve(c.Context(), c.Params("id"))
	if errors.Is(err, errNoChannel) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	v, err := api.view(c, id)
	if errors.Is(err, errNoChannel) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(v)
}

// members lists, sets or removes a channel's owners and editors; AuthKit
// checks the manager's own authority over each role.
func (api *channelAPI) members(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
	}
	allowed, err := api.allowed(c.Context(), viewer(c), id, channelSettingsManage)
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed {
		return clientError(c, 404, "channel not found")
	}
	ref, manager := iam.GroupByID(id), iam.UserIdentity(viewer(c))
	switch c.Method() {
	case "GET":
		data := []fiber.Map{}
		query := iam.MemberQuery{Kinds: []iam.SubjectKind{iam.SubjectKindUser}, Page: iam.PageRequest{Limit: 100}}
		for {
			page, err := api.auth.client.ListGroupMembers(c.Context(), ref, query)
			if err != nil {
				return billingUnavailable(c)
			}
			for _, m := range page.Items {
				data = append(data, fiber.Map{"user_id": m.Subject.ID, "subject_kind": m.Subject.Kind, "role": roleName(m.Role)})
			}
			if page.Next == "" {
				return c.JSON(fiber.Map{"data": data})
			}
			query.Page.Cursor = page.Next
		}
	case "DELETE":
		if err = api.auth.client.RemoveGroupMember(c.Context(), manager, ref, iam.UserSubject(c.Params("user_id"))); err != nil {
			return clientError(c, 409, "member cannot be removed; an active owner is required")
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
	role, ok := map[string]iam.Role{"owner": channelOwnerRole, "editor": channelEditorRole}[in.Role]
	if !ok {
		return clientError(c, 400, "role must be owner or editor")
	}
	user, err := api.auth.client.User(c.Context(), iam.UserByUsername(strings.TrimSpace(in.Username)))
	if err != nil {
		return clientError(c, 404, "user not found")
	}
	if _, err = api.auth.client.SetGroupRole(c.Context(), manager, ref, iam.UserSubject(user.ID), role); err != nil {
		return clientError(c, 403, "member could not be assigned")
	}
	return c.Status(201).JSON(fiber.Map{"user_id": user.ID, "role": in.Role})
}

// roleName is a channel role's name without its persona: owner or editor.
func roleName(role iam.Role) string {
	_, name, _ := strings.Cut(role.String(), ":")
	return name
}

func (api *postAPI) me(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	user := viewer(c)
	profile, err := api.auth.user(c.Context(), user)
	if err != nil {
		return billingUnavailable(c)
	}
	customer, err := customerID(user)
	if err != nil {
		return clientError(c, 401, "a user access token is required")
	}
	managed, err := api.channels.editable(c, user)
	if err != nil {
		return billingUnavailable(c)
	}
	before, _ := parsePostID(c.Query("before"))
	// The library is every post the viewer holds a purchase of.
	keys := []string{}
	params := billing.CustomerEntitlementListParams{Prefix: postResource(""), PageRequest: billing.PageRequest{Limit: 100}}
	for {
		held, err := api.billing.reads.ListCustomerEntitlements(c.Context(), customer, params)
		if err != nil {
			return billingUnavailable(c)
		}
		for _, e := range held.Items {
			if key, ok := strings.CutPrefix(e.Entitlement, postResource("")); ok && uuid.Validate(key) == nil {
				keys = append(keys, key)
			}
		}
		if held.Next == "" {
			break
		}
		params.Cursor = held.Next
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=ANY($2::uuid[]) AND deleted_at IS NULL`+published+` AND ($1::text='' OR id<$1::uuid) AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL) ORDER BY id DESC LIMIT 51`, before, keys)
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
		next = purchased[len(purchased)-1].ID
	}
	if err = api.decorate(c, purchased, false); err != nil {
		return billingUnavailable(c)
	}
	subscriptions, err := api.billing.client.ListSubscriptions(c.Context(), billing.SubscriptionListParams{CustomerID: customer, PageRequest: billing.PageRequest{Limit: 50}})
	if err != nil {
		return billingUnavailable(c)
	}
	payments, err := api.billing.client.ListPayments(c.Context(), billing.PaymentListParams{CustomerID: customer, PageRequest: billing.PageRequest{Limit: 50}})
	if err != nil {
		return billingUnavailable(c)
	}
	avatar, err := api.media.slots(c.Context(), media.UserKind, []string{profile.ID}, slotAvatar)
	if err != nil {
		return databaseError(c, err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"user": fiber.Map{"id": profile.ID, "username": profile.Username, "email": profile.Email, "avatar": avatar[slotKey{profile.ID, slotAvatar}]}, "manageable_channels": managed, "purchased_posts": purchased, "has_more": more, "next_cursor": next, "subscriptions": subscriptions.Items, "payments": payments.Items})
}

// editable lists the channels the user can publish to.
func (api *channelAPI) editable(c fiber.Ctx, user string) ([]channelView, error) {
	ids, err := api.auth.channels(c.Context(), user)
	if err != nil {
		return nil, err
	}
	views, err := api.views(c.Context(), user, ids, false)
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
