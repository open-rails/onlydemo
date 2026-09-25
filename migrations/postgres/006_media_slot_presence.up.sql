-- parent: 5 sha256:ed50b3e15ce5121ecab0eede351a14f2629de15dea1997b335232ffd929b6e23

-- Slot outputs are rewritten in place (ContentKit v0.40): no stamp. A row
-- means the slot is set; aspect is its "W:H" (native slots: the image's own).
-- Native aspects fill in as `onlydemo media reprocess` re-encodes each slot.
ALTER TABLE media_slots DROP COLUMN stamp, ADD COLUMN aspect TEXT NOT NULL DEFAULT '';
