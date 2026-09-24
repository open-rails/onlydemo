package main

import (
	"context"
	"log/slog"

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
	avatarSlot = media.Slot{Aspect: 1, Widths: []int{128, 256, 512}, MinWidth: 128, Quality: 85}
	coverSlot  = media.Slot{Aspect: 3, Widths: []int{1500, 3000}, MinWidth: 1500, Quality: 85}
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
		spec, ok := k.Slots[slot]
		if !ok {
			return nil
		}
		version, _, err := media.SlotStamp(stamp).Parse()
		if err != nil {
			return err
		}
		outs, err := m.reader.SlotOutputs(m.ref(kind, id), slot, media.SlotStamp(stamp))
		if err != nil {
			return err
		}
		if len(outs) > 0 {
			out[slotKey{id, slot}] = &media.SlotManifest{Aspect: spec.Aspect, Version: version, Outputs: outs}
		}
		return nil
	})
	return out, err
}

func (m *mediaService) deleteSlotsTx(ctx context.Context, tx pgx.Tx, kind, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+m.slotTable+` WHERE kind=$1 AND item_id=$2`, kind, id)
	return err
}
