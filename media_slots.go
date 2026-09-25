package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
)

// Public slots render at a small and a large width (the SDK picks by rendered
// size × 2–3× density) to immutable, hash-named files. media_slots keeps each
// set slot's SlotListing so listings link it without reads.
const (
	slotAvatar = "avatar"
	slotCover  = "cover"
)

var (
	// Two renditions each (small, large; the SDK picks by rendered size × 2–3×
	// density): avatars show at 40–96 px, channel headers up to ~1000 px wide.
	// ContentKit renders the large rung at the crop's width when it is narrower.
	avatarSlot = media.Slot{Aspect: media.Ratio("1:1"), Widths: []int{128, 512}, MinWidth: 128, Quality: 85}
	coverSlot  = media.Slot{Aspect: media.Ratio("3:1"), Widths: []int{900, 3000}, MinWidth: 600, Quality: 85}
)

type slotKey struct{ ID, Slot string }

// slotEncoded is Hooks.SlotEncoded (run by the media worker): it stores the
// slot's new listing in table.
func slotEncoded(pool *pgxpool.Pool, table string) func(context.Context, contentref.ContentRef, string, media.SlotListing) {
	return func(ctx context.Context, ref contentref.ContentRef, slot string, l media.SlotListing) {
		_, err := pool.Exec(ctx, `INSERT INTO `+table+` (kind, item_id, slot, listing) VALUES ($1,$2,$3,$4)
			ON CONFLICT (kind, item_id, slot) DO UPDATE SET listing=EXCLUDED.listing, updated_at=NOW()`,
			ref.ContentKind, ref.ContentID, slot, l)
		if err != nil {
			slog.Warn("record slot", "ref", ref.String(), "slot", slot, "err", err)
		}
	}
}

// slots returns the manifests of the named slots of kind's items that are
// set, built without bucket reads.
func (m *mediaService) slots(ctx context.Context, kind string, ids []string, names ...string) (map[slotKey]*media.SlotManifest, error) {
	out := map[slotKey]*media.SlotManifest{}
	if len(ids) == 0 {
		return out, nil
	}
	k, err := m.kinds.Kind(kind)
	if err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `SELECT item_id, slot, listing FROM `+m.slotTable+` WHERE kind=$1 AND item_id=ANY($2) AND slot=ANY($3)`, kind, ids, names)
	if err != nil {
		return nil, err
	}
	var id, slot string
	var listing media.SlotListing
	_, err = pgx.ForEachRow(rows, []any{&id, &slot, &listing}, func() error {
		if _, ok := k.Slots[slot]; !ok {
			return nil
		}
		man, err := m.reader.ListedSlot(m.ref(kind, id), slot, listing)
		if err != nil {
			return err
		}
		out[slotKey{id, slot}] = &man
		return nil
	})
	return out, err
}

func (m *mediaService) deleteSlotsTx(ctx context.Context, tx pgx.Tx, kind, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+m.slotTable+` WHERE kind=$1 AND item_id=$2`, kind, id)
	return err
}

// videoImages sets each published post's poster; drafts and publishing posts
// are hidden, so their covers have no public copy. Playable videos preview inline from their HLS
// (SDK MediaGallery).
func (m *mediaService) videoImages(ctx context.Context, posts []post) error {
	ids := make([]string, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	posters, err := m.slots(ctx, kindPost, ids, media.PosterSlot)
	if err != nil {
		return err
	}
	for i := range posts {
		poster := posters[slotKey{ids[i], media.PosterSlot}]
		if poster == nil || posts[i].State != statePublished {
			continue
		}
		posts[i].Poster = poster
	}
	return nil
}
