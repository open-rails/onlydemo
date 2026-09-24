import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useUpload } from "@open-rails/contentkit-upload/react";
import type { RefBody } from "@open-rails/contentkit-upload";
import { HugeiconsIcon } from "@hugeicons/react";
import { Camera01Icon } from "@hugeicons/core-free-icons";
import { Spinner } from "@/components/ui/spinner";
import { readChannel, uploadMessage, uploads, withMediaType } from "../media";
import { CropDialog } from "./crop-dialog";

export function SlotUpload({
  target,
  slot,
  label,
  onDone,
  className,
  iconOnly,
}: {
  target: RefBody;
  slot: string;
  label: string;
  onDone: () => void;
  className?: string;
  iconOnly?: boolean;
}) {
  const up = useUpload(uploads);
  return (
    <label className={className ?? "slot-upload"} title={up.error ? uploadMessage(up.error) : label}>
      {up.status === "uploading" ? <Spinner /> : <HugeiconsIcon icon={Camera01Icon} size={16} />}
      {up.status === "error" ? (
        <span>{uploadMessage(up.error)}</span>
      ) : (
        <span className={iconOnly ? "sr-only" : undefined}>{label}</span>
      )}
      <input
        type="file"
        accept="image/jpeg,image/png,image/webp,image/gif"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) up.upload(file, { ref: target, slot }).then(onDone, () => {});
        }}
      />
    </label>
  );
}

// A channel slot cropped by ContentKit: the picked image becomes the
// channel's "{slot}-source" file, the creator crops and rotates it, and the
// slot is set from that file (commit-slot-from-file) at the slot's aspect.
export function SlotCropUpload({
  channel,
  slot,
  aspect,
  label,
  onDone,
  className,
  iconOnly,
}: {
  channel: string;
  slot: string;
  aspect: number;
  label: string;
  onDone: () => void;
  className?: string;
  iconOnly?: boolean;
}) {
  const target = { kind: "channel", id: channel };
  const name = `${slot}-source`;
  const [phase, setPhase] = useState<"idle" | "uploading" | "cropping">("idle");
  const [error, setError] = useState("");
  const view = useQuery({
    queryKey: ["slot-source", channel, slot],
    queryFn: () => readChannel(channel),
    enabled: phase === "cropping",
    refetchInterval: (q) => {
      const f = q.state.data?.files.find((x) => x.name === name);
      return f?.url && f.dims ? false : 1500;
    },
  });
  const source = view.dataUpdatedAt ? view.data?.files.find((f) => f.name === name) : undefined;
  const pick = async (file: File) => {
    setError("");
    setPhase("uploading");
    try {
      const up = await uploads.upload(withMediaType(file), { ref: target });
      const exists = (await readChannel(channel)).files.some((f) => f.name === name);
      await uploads.commit(target, [{ op: exists ? "replace" : "insert", name, original: up.name }], {
        sources: { [up.name]: file },
      });
      await view.refetch();
      setPhase("cropping");
    } catch (e) {
      setError(uploadMessage(e));
      setPhase("idle");
    }
  };
  return (
    <>
      <label className={className ?? "slot-upload"} title={error || label}>
        {phase === "uploading" ? <Spinner /> : <HugeiconsIcon icon={Camera01Icon} size={16} />}
        {error ? <span>{error}</span> : <span className={iconOnly ? "sr-only" : undefined}>{label}</span>}
        <input
          type="file"
          accept="image/jpeg,image/png,image/webp,image/gif"
          hidden
          onChange={(e) => {
            const file = e.target.files?.[0];
            e.target.value = "";
            if (file) void pick(file);
          }}
        />
      </label>
      {phase === "cropping" && (
        <CropDialog
          title={label}
          src={source?.url}
          dims={source?.dims}
          aspect={aspect}
          onSave={(edit) => uploads.setSlotFromFile(target, slot, name, edit ?? {}).then(onDone)}
          onClose={() => setPhase("idle")}
        />
      )}
    </>
  );
}
