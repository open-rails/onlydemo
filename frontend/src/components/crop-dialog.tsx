import { useEffect, useMemo, useState } from "react";
import Cropper from "react-easy-crop";
import { useCrop } from "@open-rails/contentkit-upload/react";
import type { Edit } from "@open-rails/contentkit-upload";
import { HugeiconsIcon } from "@hugeicons/react";
import { RotateClockwiseIcon, RotateLeft01Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { FormError } from "./states";

export interface Dims {
  w: number;
  h: number;
}

// Edited-image aspects offered for post images; slots pass their own.
const presets: [string, number | undefined][] = [
  ["Original", undefined],
  ["1:1", 1],
  ["4:5", 4 / 5],
  ["16:9", 16 / 9],
];

function useNaturalSize(src: string) {
  const [size, setSize] = useState<{ src: string; width: number; height: number }>();
  useEffect(() => {
    const img = new Image();
    img.onload = () => setSize({ src, width: img.naturalWidth, height: img.naturalHeight });
    img.src = src;
  }, [src]);
  return size?.src === src ? size : undefined;
}

// Crop and rotate are ContentKit edits: the crop is kept in original pixels
// (the SDK's useCrop), drawn here on the unedited "editor" variant, and the
// clockwise rotation applies after it. The original is never changed.
function CropEditor({
  src,
  display,
  dims,
  aspect,
  initial,
  onEdit,
}: {
  src: string;
  display: { width: number; height: number };
  dims: Dims;
  aspect?: number;
  initial?: Edit | null;
  onEdit: (edit: Edit | null) => void;
}) {
  const source = useMemo(() => ({ width: dims.w, height: dims.h }), [dims.w, dims.h]);
  const c = useCrop({ source, aspect, initial });
  const [pos, setPos] = useState({ x: 0, y: 0 });
  const [zoom, setZoom] = useState(1);
  const [resets, setResets] = useState(0);
  const scale = display.width / dims.w;
  useEffect(() => onEdit(c.edit), [c.edit, onEdit]);
  const box = 140 / Math.max(c.crop.w, c.crop.h);
  const turned = c.rotate % 180 !== 0;
  // The cropper draws the unrotated source: a fixed edited aspect transposes
  // with the rotation; without one the starting crop's shape is kept.
  const [free] = useState(() => c.crop.w / c.crop.h);
  const ratio = aspect ? (turned ? 1 / aspect : aspect) : free;
  return (
    <>
      <div className="crop-area">
        <Cropper
          key={`${c.rotate}-${resets}`}
          image={src}
          crop={pos}
          zoom={zoom}
          maxZoom={8}
          aspect={ratio}
          initialCroppedAreaPixels={{
            x: c.crop.x * scale,
            y: c.crop.y * scale,
            width: c.crop.w * scale,
            height: c.crop.h * scale,
          }}
          onCropChange={setPos}
          onZoomChange={setZoom}
          onCropComplete={(_, px) =>
            c.setFromDisplay({ x: px.x, y: px.y, w: px.width, h: px.height }, display)
          }
        />
      </div>
      <div className="crop-tools">
        <Button size="icon-sm" variant="outline" aria-label="Rotate left" onClick={() => c.rotateBy(-90)}>
          <HugeiconsIcon icon={RotateLeft01Icon} />
        </Button>
        <Button size="icon-sm" variant="outline" aria-label="Rotate right" onClick={() => c.rotateBy(90)}>
          <HugeiconsIcon icon={RotateClockwiseIcon} />
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            c.reset();
            setZoom(1);
            setResets((n) => n + 1);
          }}
        >
          Reset
        </Button>
        <span className="muted text-sm">
          {c.crop.w}×{c.crop.h}
          {c.rotate ? `, ${c.rotate}°` : ""}
        </span>
        <span
          className="crop-preview"
          aria-label="Result preview"
          style={{
            width: (turned ? c.crop.h : c.crop.w) * box,
            height: (turned ? c.crop.w : c.crop.h) * box,
          }}
        >
          <span
            style={{
              width: c.crop.w * box,
              height: c.crop.h * box,
              backgroundImage: `url("${src}")`,
              backgroundSize: `${dims.w * box}px ${dims.h * box}px`,
              backgroundPosition: `${-c.crop.x * box}px ${-c.crop.y * box}px`,
              transform: `translate(-50%, -50%) rotate(${c.rotate}deg)`,
            }}
          />
        </span>
      </div>
    </>
  );
}

export function CropDialog({
  title,
  src,
  dims,
  aspect,
  initial,
  onSave,
  onClose,
}: {
  title: string;
  src?: string;
  dims?: Dims;
  aspect?: number;
  initial?: Edit | null;
  onSave: (edit: Edit | null) => Promise<unknown>;
  onClose: () => void;
}) {
  const display = useNaturalSize(src ?? "");
  const [preset, setPreset] = useState<number | undefined | null>(null);
  const [edit, setEdit] = useState<Edit | null>(initial ?? null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const chosen = aspect ?? (preset === null ? undefined : preset);
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="crop-dialog sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            Drag and zoom to crop, then rotate. The original upload is kept.
          </DialogDescription>
        </DialogHeader>
        {aspect === undefined && (
          <div className="crop-tools">
            {presets.map(([label, value]) => (
              <Button
                key={label}
                size="sm"
                variant={(preset ?? undefined) === value && preset !== null ? "secondary" : "ghost"}
                onClick={() => setPreset(value)}
              >
                {label}
              </Button>
            ))}
          </div>
        )}
        {src && dims && display ? (
          <CropEditor
            key={String(chosen)}
            src={src}
            display={display}
            dims={dims}
            aspect={chosen}
            initial={preset === null ? initial : null}
            onEdit={setEdit}
          />
        ) : (
          <p className="muted text-sm">
            <Spinner className="inline" /> Preparing the image…
          </p>
        )}
        <FormError>{error}</FormError>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={saving || !dims}
            onClick={() => {
              setSaving(true);
              setError("");
              onSave(edit).then(onClose, (e: unknown) => {
                setSaving(false);
                setError(e instanceof Error ? e.message : String(e));
              });
            }}
          >
            {saving && <Spinner data-icon="inline-start" />}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
