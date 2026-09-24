import { useRef } from "react";
import type { RefBody, SlotManifest } from "@openrails/contentkit-upload";
import { useSlotCrop } from "@openrails/contentkit-upload/react";
import { ImageCropDialog, useMessages } from "@openrails/contentkit-upload/ui";
import { HugeiconsIcon } from "@hugeicons/react";
import { Camera01Icon, CropIcon, ImageUpload01Icon } from "@hugeicons/core-free-icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Spinner } from "@/components/ui/spinner";
import { uploads, useSlotSaved } from "../media";

// A compact slot editor for layouts where the image is drawn elsewhere (the
// channel header): pick → crop → save, or re-crop the kept original. The
// ContentKit UI renders the dialog; outputs are rendered server-side.
export function SlotEdit({
  item,
  slot,
  manifest,
  aspect,
  targetWidth,
  label,
  iconOnly,
  className,
}: {
  item: RefBody;
  slot: string;
  manifest: SlotManifest | null;
  aspect: number;
  targetWidth: number;
  label: string;
  iconOnly?: boolean;
  className?: string;
}) {
  const { t, error } = useMessages();
  const saved = useSlotSaved();
  const crop = useSlotCrop(uploads, {
    ref: item,
    slot,
    manifest,
    aspect,
    onSaved: (m) => saved(item, slot, m),
  });
  const input = useRef<HTMLInputElement>(null);
  const busy = crop.status === "decoding" || crop.status === "saving";
  const failed = crop.status === "error" && !crop.source ? error(crop.error) : "";
  const content = (
    <>
      {busy ? <Spinner /> : <HugeiconsIcon icon={Camera01Icon} strokeWidth={2} />}
      <span className={iconOnly ? "sr-only" : undefined}>{label}</span>
    </>
  );
  return (
    <>
      {manifest?.outputs.length && crop.canRecrop ? (
        <DropdownMenu>
          <DropdownMenuTrigger className={className} disabled={busy} title={label}>
            {content}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-auto">
            <DropdownMenuItem onClick={() => input.current?.click()}>
              <HugeiconsIcon icon={ImageUpload01Icon} strokeWidth={2} />
              {t("common.change")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => void crop.recrop()}>
              <HugeiconsIcon icon={CropIcon} strokeWidth={2} />
              {t("common.editCrop")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <button type="button" className={className} disabled={busy} title={label} onClick={() => input.current?.click()}>
          {content}
        </button>
      )}
      {failed && (
        <span role="alert" className="slot-error">
          {failed}
        </span>
      )}
      <input
        ref={input}
        type="file"
        accept="image/*"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) void crop.pick(file);
        }}
      />
      <ImageCropDialog
        open={"source" in crop && !!crop.source}
        onOpenChange={(open) => !open && crop.cancel()}
        source={"source" in crop ? (crop.source ?? null) : null}
        aspect={aspect}
        round={aspect === 1}
        initialEdit={"edit" in crop && crop.mode === "recrop" ? crop.edit : undefined}
        targetWidth={targetWidth}
        title={t(aspect === 1 ? "crop.avatarTitle" : "crop.coverTitle")}
        busy={crop.status === "saving"}
        progress={crop.status === "saving" ? crop.progress : undefined}
        rendering={crop.status === "saving" && crop.rendering}
        error={crop.status === "error" && crop.source ? error(crop.error) : undefined}
        // No onEditChange: the dialog reports a new edit object on every
        // render (SDK v0.22.0), so mirroring it into useSlotCrop loops.
        onConfirm={(edit) => void crop.save(edit)}
      />
    </>
  );
}
