package main

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
)

// Public slots: each kind's slots render at several widths so a CSS box
// ships 2x/3x pixels. Listings read each slot's stamp from media_slots; URLs
// carry ?v= and are immutable per version.
const (
	slotAvatar = "avatar"
	slotCover  = "cover"
)

var (
	// Two renditions each (small, large; the SDK picks by rendered size × 2–3×
	// density): avatars show at 40–96 px, channel headers up to ~1000 px wide.
	// ContentKit renders the large rung at the crop's width when it is narrower.
	avatarSlot = media.Slot{Aspect: 1, Widths: []int{128, 512}, MinWidth: 128, Quality: 85}
	coverSlot  = media.Slot{Aspect: 3, Widths: []int{900, 3000}, MinWidth: 600, Quality: 85}
)

type slotKey struct{ ID, Slot string }

// slotEncoded is Hooks.SlotEncoded: it stores the slot's stamp, the one value
// listings need to render every output.
func (m *mediaService) slotEncoded(ctx context.Context, ref contentref.ContentRef, slot string, stamp media.SlotStamp) {
	_, err := m.pool.Exec(ctx, `INSERT INTO `+m.slotTable+` (kind, item_id, slot, stamp) VALUES ($1,$2,$3,$4)
		ON CONFLICT (kind, item_id, slot) DO UPDATE SET stamp=EXCLUDED.stamp, updated_at=NOW()`,
		ref.ContentKind, ref.ContentID, slot, string(stamp))
	if err != nil {
		slog.Warn("record slot stamp", "ref", ref.String(), "slot", slot, "err", err)
	}
}

// slots returns the manifests of the named slots of kind's items, built from
// their stamps without bucket reads; a slot never set is absent.
func (m *mediaService) slots(ctx context.Context, kind string, ids []string, names ...string) (map[slotKey]*media.SlotManifest, error) {
	out := map[slotKey]*media.SlotManifest{}
	if len(ids) == 0 {
		return out, nil
	}
	k, err := m.kinds.Kind(kind)
	if err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `SELECT item_id, slot, stamp FROM `+m.slotTable+` WHERE kind=$1 AND item_id=ANY($2) AND slot=ANY($3)`, kind, ids, names)
	if err != nil {
		return nil, err
	}
	var id, slot, stamp string
	_, err = pgx.ForEachRow(rows, []any{&id, &slot, &stamp}, func() error {
		if _, ok := k.Slots[slot]; !ok {
			return nil
		}
		man, err := m.reader.StampedSlot(m.ref(kind, id), slot, media.SlotStamp(stamp))
		if err != nil {
			return err
		}
		if len(man.Outputs) > 0 {
			out[slotKey{id, slot}] = &man
		}
		return nil
	})
	return out, err
}

func (m *mediaService) deleteSlotsTx(ctx context.Context, tx pgx.Tx, kind, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+m.slotTable+` WHERE kind=$1 AND item_id=$2`, kind, id)
	return err
}

// hoverPreview is a post's silent loop: the smallest MP4 (preferred) and WebP.
type hoverPreview struct {
	MP4  string `json:"mp4"`
	WebP string `json:"webp"`
}

// videoImages sets each published post's poster from its stored stamp and,
// for public posts, its hover preview: only what ContentKit publishes
// (paid posts show the poster alone; drafts nothing). Preview URLs are
// unversioned: public/ is served no-cache and the render is not reported back.
func (m *mediaService) videoImages(ctx context.Context, posts []post) error {
	ids := make([]string, len(posts))
	for i, p := range posts {
		ids[i] = strconv.FormatInt(p.ID, 10)
	}
	posters, err := m.slots(ctx, kindPost, ids, media.PosterSlot)
	if err != nil {
		return err
	}
	for i := range posts {
		poster := posters[slotKey{ids[i], media.PosterSlot}]
		if poster == nil || posts[i].Draft {
			continue
		}
		posts[i].Poster = poster
		if posts[i].AccessPolicy != "public" {
			continue
		}
		mp4, webp, err := m.reader.HoverPreviewURLs(m.postRef(posts[i].ID), "")
		if err != nil {
			return err
		}
		posts[i].HoverPreview = &hoverPreview{MP4: mp4, WebP: webp}
	}
	return nil
}
