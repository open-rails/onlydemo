package main

import (
	"errors"
	"github.com/gofiber/fiber/v3"
	"github.com/open-rails/authkit"
	"github.com/open-rails/openrails"
	"strconv"
	"strings"
)

type channelView struct {
	ID            string                   `json:"id"`
	Slug          string                   `json:"slug"`
	Name          string                   `json:"name"`
	Role          string                   `json:"role,omitempty"`
	CanManage     bool                     `json:"can_manage"`
	CanEdit       bool                     `json:"can_edit"`
	HasMembership bool                     `json:"has_membership"`
	Offers        []openrails.CatalogOffer `json:"offers"`
}

func (api *channelAPI) view(c fiber.Ctx, id string, offers bool) (channelView, error) {
	active, err := api.active(c.Context(), id)
	if err != nil {
		return channelView{}, err
	}
	if !active {
		return channelView{}, authkit.ErrGroupNotFound
	}
	group, err := api.auth.client.GroupInstanceByID(c.Context(), id)
	if err != nil {
		return channelView{}, err
	}
	if group.DeletedAt != nil {
		return channelView{}, authkit.ErrGroupNotFound
	}
	v := channelView{ID: id, Slug: group.InstanceSlug, Name: group.DisplayName, Offers: []openrails.CatalogOffer{}}
	user := viewer(c)
	if user != "" {
		v.CanManage, err = api.allowed(c.Context(), user, id, "channel:settings:manage")
		if err != nil {
			return v, err
		}
		v.CanEdit, err = api.allowed(c.Context(), user, id, channelEditPermission)
		if err != nil {
			return v, err
		}
		if v.CanManage {
			v.Role = "owner"
		} else if v.CanEdit {
			v.Role = "editor"
		}
		access, e := api.billing.access(c.Context(), user, []string{membershipResource(id)})
		if e != nil {
			return v, e
		}
		v.HasMembership = access[membershipResource(id)]
	}
	if offers {
		v.Offers, err = api.billing.offers(c.Context(), membershipResource(id), true)
	}
	return v, err
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
	data := []channelView{}
	for _, id := range ids {
		v, e := api.view(c, id, false)
		if errors.Is(e, authkit.ErrGroupNotFound) {
			continue
		}
		if e != nil {
			return billingUnavailable(c)
		}
		data = append(data, v)
	}
	next := ""
	if more {
		next = ids[len(ids)-1]
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"data": data, "has_more": more, "next_cursor": next})
}
func (api *channelAPI) publicGet(c fiber.Ctx) error {
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
	}
	v, err := api.view(c, id, true)
	if errors.Is(err, authkit.ErrGroupNotFound) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(v)
}
func (api *channelAPI) membership(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
	}
	release, err := api.lock(c.Context(), id)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.allowed(c.Context(), viewer(c), id, "channel:settings:manage")
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed {
		return clientError(c, 404, "channel not found")
	}
	var price offerPrice
	if err = c.Bind().Body(&price); err != nil {
		return clientError(c, 400, "invalid price")
	}
	group, err := api.auth.client.GroupInstanceByID(c.Context(), id)
	if err != nil {
		return clientError(c, 404, "channel not found")
	}
	if err = api.billing.setOffer(c.Context(), id, membershipResource(id), group.DisplayName+" membership", &price, true, false); err != nil {
		return clientError(c, 400, "membership offer could not be saved")
	}
	v, err := api.view(c, id, true)
	if err != nil {
		return billingUnavailable(c)
	}
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
	groups, err := api.auth.client.ListSubjectGroups(c.Context(), authkit.UserSubject(user))
	if err != nil {
		return billingUnavailable(c)
	}
	managed := []channelView{}
	for _, g := range groups {
		if g.Persona != channelPersona {
			continue
		}
		v, e := api.channels.view(c, g.GroupID, false)
		if errors.Is(e, authkit.ErrGroupNotFound) {
			continue
		}
		if e != nil {
			return billingUnavailable(c)
		}
		if v.CanEdit {
			managed = append(managed, v)
		}
	}
	before := int64(0)
	if raw := c.Query("before"); raw != "" {
		before, _ = strconv.ParseInt(raw, 10, 64)
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE ($1::bigint=0 OR id<$1) AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL) ORDER BY id DESC LIMIT 51`, before)
	if err != nil {
		return databaseError(c, err)
	}
	posts := []post{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return databaseError(c, e)
		}
		posts = append(posts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return databaseError(c, err)
	}
	more := len(posts) > 50
	if more {
		posts = posts[:50]
	}
	next := ""
	if more {
		next = strconv.FormatInt(posts[len(posts)-1].ID, 10)
	}
	if err = api.decorate(c, posts, false); err != nil {
		return billingUnavailable(c)
	}
	purchased := []post{}
	for _, p := range posts {
		if p.Purchased {
			purchased = append(purchased, p)
		}
	}
	subscriptions, err := api.billing.client.ListSubscriptions(c.Context(), openrails.SubscriptionFilter{CustomerID: user, PageOptions: openrails.PageOptions{Limit: 50}})
	if err != nil {
		return billingUnavailable(c)
	}
	payments, err := api.billing.client.ListPayments(c.Context(), openrails.PaymentFilter{CustomerID: user, PageOptions: openrails.PageOptions{Limit: 50}})
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"user": fiber.Map{"id": profile.ID, "username": profile.Username, "email": profile.Email}, "manageable_channels": managed, "purchased_posts": purchased, "has_more": more, "next_cursor": next, "subscriptions": subscriptions.Data, "payments": payments.Data})
}
