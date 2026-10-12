package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/open-rails/authkit/iam"
)

// channelAPI composes a channel from its two halves: the ContentKit group
// (handle, name, images, membership offer) and the AuthKit group that is its
// auth_scope (owners and editors). Everything else about a channel is
// ContentKit's and AuthKit's own routes. ContentKit provisions the AuthKit
// half itself once its adapters/authkit lands (docs/contentkit-cutover.md).
type channelAPI struct {
	auth *appAuth
	ck   *ckAPI
}

func (api *channelAPI) mount(app fiber.Router, required fiber.Handler) {
	app.Post("/api/v1/channels", required, api.create)
	app.Delete("/api/v1/channels/:id", required, api.delete)
}

// create makes the viewer's channel: its AuthKit group, owned by the viewer,
// then its ContentKit group with the viewer's own credential. A refused group
// takes the AuthKit group back.
func (api *channelAPI) create(c fiber.Ctx) error {
	user := viewer(c)
	if user == "" {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	var in struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := bindJSON(c, &in); err != nil {
		return clientError(c, http.StatusBadRequest, "expected {slug, name}")
	}
	in.Slug, in.Name = strings.ToLower(strings.TrimSpace(in.Slug)), strings.TrimSpace(in.Name)
	if in.Slug == "" || in.Name == "" {
		return clientError(c, http.StatusBadRequest, "slug and name are required")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return unavailable(c)
	}
	owner := iam.UserSubject(user)
	ctx := c.Context()
	if _, err = api.auth.client.CreateGroup(ctx, iam.NewGroup{ID: id.String(), Persona: channelPersona.Persona, Owner: &owner}); err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	var created []json.RawMessage
	err = api.ck.call(ctx, http.MethodPost, "/v1/groups", c.Get("Authorization"), []map[string]any{{
		"id": id.String(), "kind": kindChannel, "slug": in.Slug, "owner_user_id": user, "auth_scope": id.String(),
		"names": []map[string]string{{"language": "en", "kind": "name", "name": in.Name}},
	}}, &created)
	if err != nil {
		if rerr := api.retire(context.WithoutCancel(ctx), id.String(), true); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return ckFail(c, err)
	}
	return c.Status(http.StatusCreated).Send(created[0])
}

// delete withdraws the channel's membership offer (members and subscribers
// keep theirs), deletes its ContentKit group, then retires its AuthKit group.
func (api *channelAPI) delete(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return clientError(c, http.StatusNotFound, "channel not found")
	}
	ctx, cred := c.Context(), c.Get("Authorization")
	if err = api.ck.call(ctx, http.MethodDelete, "/v1/groups/"+id.String()+"/offer", cred, nil, nil); err != nil && ckStatus(err) != http.StatusNotFound {
		return ckFail(c, err)
	}
	var deleted json.RawMessage
	if err = api.ck.call(ctx, http.MethodDelete, "/v1/groups/"+id.String(), cred, nil, &deleted); err != nil {
		return ckFail(c, err)
	}
	if err = api.retire(ctx, id.String(), false); err != nil {
		return clientError(c, http.StatusServiceUnavailable, "channel retirement is pending")
	}
	return c.Send(deleted)
}

// retire soft-deletes the channel's AuthKit group, so its roles grant
// nothing; purge also removes it, for a group ContentKit never stored.
func (api *channelAPI) retire(ctx context.Context, id string, purge bool) error {
	err := api.auth.client.DeleteGroup(ctx, iam.GroupByID(id))
	if err == nil && purge {
		err = api.auth.client.PurgeGroup(ctx, iam.GroupByID(id))
	}
	if errors.Is(err, iam.ErrGroupNotFound) {
		return nil
	}
	return err
}

// ckFail answers a refused ContentKit call with its status and code.
func ckFail(c fiber.Ctx, err error) error {
	var e *ckError
	if errors.As(err, &e) {
		return c.Status(e.Status).JSON(fiber.Map{"error": e.Msg, "code": e.Code})
	}
	return unavailable(c)
}
