package main

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/authkit"
	"github.com/open-rails/contentkit/media"
)

// page is what a listing shows its viewer about its channels, read in a fixed
// number of queries and calls however many items it holds.
type page struct {
	groups map[string]authkit.GroupInstance
	states map[string]membershipState // live channels only
	// perms are the viewer's grants on each channel, root grants included.
	perms  map[string][]authkit.Perm
	slots  map[slotKey]*media.SlotManifest
	access map[string]bool
}

func (pg *page) can(channel string, perm authkit.Perm) bool {
	return slices.ContainsFunc(pg.perms[channel], perm.Matches)
}

// live reports a channel whose row and AuthKit group both still exist.
func (pg *page) live(channel string) bool {
	_, ok := pg.states[channel]
	g, found := pg.groups[channel]
	return ok && found && g.DeletedAt == nil
}

// loadPage reads channels (known: groups the caller already has), the
// viewer's grants on them, the named channel slots and the viewer's
// entitlements among keys.
func (api *channelAPI) loadPage(ctx context.Context, user string, ids []string, known map[string]authkit.GroupInstance, keys []string, slots ...string) (*page, error) {
	pg := &page{groups: map[string]authkit.GroupInstance{}, states: map[string]membershipState{}, perms: map[string][]authkit.Perm{}}
	ids = unique(ids)
	var err error
	if pg.access, err = api.billing.access(ctx, user, keys); err != nil || len(ids) == 0 {
		pg.slots = map[slotKey]*media.SlotManifest{}
		return pg, err
	}
	rows, err := api.pool.Query(ctx, `SELECT id::text,membership,membership_free,membership_sync,membership_revision FROM `+api.table+` WHERE id=ANY($1::uuid[]) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	var id string
	var s membershipState
	if _, err = pgx.ForEachRow(rows, []any{&id, &s.Status, &s.Free, &s.Sync, &s.Revision}, func() error { pg.states[id] = s; return nil }); err != nil {
		return nil, err
	}
	if pg.slots, err = api.media.slots(ctx, kindChannel, ids, slots...); err != nil {
		return nil, err
	}
	missing := []string{}
	for _, id := range ids {
		if g, ok := known[id]; ok {
			pg.groups[id] = g
		} else {
			missing = append(missing, id)
		}
	}
	for chunk := range slices.Chunk(missing, authkit.MaxGroupBatch) {
		groups, err := api.auth.client.GroupInstancesByIDs(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for id, g := range groups {
			pg.groups[id] = g
		}
	}
	if user == "" {
		return pg, nil
	}
	for chunk := range slices.Chunk(ids, authkit.MaxGroupBatch) {
		perms, err := api.auth.client.EffectivePermissionsForGroups(ctx, authkit.UserSubject(user), chunk)
		if err != nil {
			return nil, err
		}
		for id, p := range perms {
			pg.perms[id] = p
		}
	}
	return pg, nil
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
