package main

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/media"
)

// channelInfo is a live channel's address and name.
type channelInfo struct {
	Slug, Name string
}

// page is what a listing shows its viewer about its channels, read in a fixed
// number of queries and calls however many items it holds.
type page struct {
	channels map[string]channelInfo
	states   map[string]membershipState // live channels only
	// perms are the viewer's grants on each channel, root grants included.
	perms  map[string][]iam.Perm
	slots  map[slotKey]*media.SlotManifest
	access map[string]bool
}

func (pg *page) can(channel string, perm iam.Perm) bool {
	return slices.ContainsFunc(pg.perms[channel], perm.Matches)
}

// live reports a channel whose row is not deleted.
func (pg *page) live(channel string) bool {
	_, ok := pg.states[channel]
	return ok
}

// loadPage reads channels, the viewer's grants on them, the named channel
// slots and the viewer's entitlements among keys.
func (api *channelAPI) loadPage(ctx context.Context, user string, ids []string, keys []string, slots ...string) (*page, error) {
	pg := &page{channels: map[string]channelInfo{}, states: map[string]membershipState{}, perms: map[string][]iam.Perm{}}
	ids = unique(ids)
	var err error
	if pg.access, err = api.billing.access(ctx, user, keys); err != nil || len(ids) == 0 {
		pg.slots = map[slotKey]*media.SlotManifest{}
		return pg, err
	}
	rows, err := api.pool.Query(ctx, `SELECT id::text,slug,name,membership,membership_free,membership_sync,membership_revision FROM `+api.table+` WHERE id=ANY($1::uuid[]) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	var id string
	var info channelInfo
	var s membershipState
	if _, err = pgx.ForEachRow(rows, []any{&id, &info.Slug, &info.Name, &s.Status, &s.Free, &s.Sync, &s.Revision}, func() error {
		pg.states[id], pg.channels[id] = s, info
		return nil
	}); err != nil {
		return nil, err
	}
	if pg.slots, err = api.media.slots(ctx, kindChannel, ids, slots...); err != nil {
		return nil, err
	}
	if pg.perms, err = api.auth.effective(ctx, user, ids); err != nil {
		return nil, err
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
