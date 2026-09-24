package main

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
)

// Public slots: each kind's slots render at several widths so a CSS box
// ships 2x/3x pixels. Listings read the current version and widths from
// media_slots; URLs carry ?v= and are immutable per version.
const (
	slotAvatar = "avatar"
	slotCover  = "cover"
)

var (
	avatarSlot = media.Slot{Aspect: 1, Widths: []int{128, 256, 512}, MinWidth: 128, Quality: 85}
	coverSlot  = media.Slot{Aspect: 3, Widths: []int{1500, 3000}, MinWidth: 1500, Quality: 85}
)

type slotKey struct{ ID, Slot string }

// slotEncoded is Hooks.SlotEncoded: it records the slot's current outputs.
func (m *mediaService) slotEncoded(ctx context.Context, ref contentref.ContentRef, slot, _ string) {
	man, err := m.manifests.SlotManifest(ctx, m.cfg.URL, ref, slot)
	if err == nil && man.Version != "" {
		widths := make([]int32, len(man.Outputs))
		for i, o := range man.Outputs {
			widths[i] = int32(o.W)
		}
		_, err = m.pool.Exec(ctx, `INSERT INTO `+m.slotTable+` (kind, item_id, slot, version, widths) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (kind, item_id, slot) DO UPDATE SET version=EXCLUDED.version, widths=EXCLUDED.widths, updated_at=NOW()`,
			ref.ContentKind, ref.ContentID, slot, man.Version, widths)
	}
	if err != nil {
		slog.Warn("record slot version", "ref", ref.String(), "slot", slot, "err", err)
	}
}

// slots returns the manifests of the named slots of kind's items; a slot never
// set is absent.
func (m *mediaService) slots(ctx context.Context, kind string, ids []string, names ...string) (map[slotKey]*media.SlotManifest, error) {
	out := map[slotKey]*media.SlotManifest{}
	if len(ids) == 0 {
		return out, nil
	}
	k, err := m.kinds.Kind(kind)
	if err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `SELECT item_id, slot, version, widths FROM `+m.slotTable+` WHERE kind=$1 AND item_id=ANY($2) AND slot=ANY($3)`, kind, ids, names)
	if err != nil {
		return nil, err
	}
	var id, slot, version string
	var widths []int32
	_, err = pgx.ForEachRow(rows, []any{&id, &slot, &version, &widths}, func() error {
		spec, ok := k.Slots[slot]
		item, err := m.kinds.Item(m.ref(kind, id))
		if !ok || err != nil {
			return err
		}
		man := &media.SlotManifest{Aspect: spec.Aspect, Version: version, Outputs: []media.SlotImage{}}
		for _, w := range widths {
			if !slices.Contains(spec.Widths, int(w)) {
				continue
			}
			key, err := item.SlotOutput(slot, int(w))
			if err != nil {
				return err
			}
			man.Outputs = append(man.Outputs, media.SlotImage{Name: media.SlotOutput(slot, int(w)), W: int(w), H: spec.Height(int(w)),
				URL: strings.TrimRight(m.cfg.URL, "/") + "/" + key + "?" + media.SlotVersionParam + "=" + version})
		}
		if len(man.Outputs) > 0 {
			out[slotKey{id, slot}] = man
		}
		return nil
	})
	return out, err
}

func (m *mediaService) deleteSlotsTx(ctx context.Context, tx pgx.Tx, kind, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+m.slotTable+` WHERE kind=$1 AND item_id=$2`, kind, id)
	return err
}
