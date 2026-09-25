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
// size × 2–3× density), rewritten in place at fixed URLs that the media host
// serves no-cache with an ETag. media_slots records which slots are set (and
// a native slot's aspect) so listings link them without reads.
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

// slotEncoded is Hooks.SlotEncoded (run by the media worker): it records in
// table that the slot is set, with its aspect.
func slotEncoded(pool *pgxpool.Pool, table string) func(context.Context, contentref.ContentRef, string, media.Aspect) {
	return func(ctx context.Context, ref contentref.ContentRef, slot string, aspect media.Aspect) {
		a, _ := aspect.MarshalText()
		_, err := pool.Exec(ctx, `INSERT INTO `+table+` (kind, item_id, slot, aspect) VALUES ($1,$2,$3,$4)
			ON CONFLICT (kind, item_id, slot) DO UPDATE SET aspect=EXCLUDED.aspect, updated_at=NOW()`,
			ref.ContentKind, ref.ContentID, slot, string(a))
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
	rows, err := m.pool.Query(ctx, `SELECT item_id, slot, aspect FROM `+m.slotTable+` WHERE kind=$1 AND item_id=ANY($2) AND slot=ANY($3)`, kind, ids, names)
	if err != nil {
		return nil, err
	}
	var id, slot, aspect string
	_, err = pgx.ForEachRow(rows, []any{&id, &slot, &aspect}, func() error {
		if _, ok := k.Slots[slot]; !ok {
			return nil
		}
		a, _ := media.ParseAspect(aspect)
		man, err := m.reader.ListedSlot(m.ref(kind, id), slot, a)
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

// hoverPreview is a post's silent loop: the smallest MP4 (preferred) and WebP.
type hoverPreview struct {
	MP4  string `json:"mp4"`
	WebP string `json:"webp"`
}

// videoImages sets each published post's poster and, for public posts, its
// hover preview: only what ContentKit publishes (paid posts show the poster
// alone; drafts nothing). URLs are fixed; public/ is served no-cache.
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
		if poster == nil || posts[i].Draft {
			continue
		}
		posts[i].Poster = poster
		if posts[i].AccessPolicy != "public" {
			continue
		}
		mp4, webp, err := m.reader.HoverPreviewURLs(m.postRef(posts[i].ID))
		if err != nil {
			return err
		}
		posts[i].HoverPreview = &hoverPreview{MP4: mp4, WebP: webp}
	}
	return nil
}
