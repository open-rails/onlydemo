import { useUpload } from "@open-rails/contentkit-upload/react";
import type { RefBody } from "@open-rails/contentkit-upload";
import { HugeiconsIcon } from "@hugeicons/react";
import { Camera01Icon } from "@hugeicons/core-free-icons";
import { Spinner } from "@/components/ui/spinner";
import { uploadMessage, uploads } from "../media";

export function SlotUpload({
  target,
  slot,
  label,
  onDone,
  className,
}: {
  target: RefBody;
  slot: string;
  label: string;
  onDone: () => void;
  className?: string;
}) {
  const up = useUpload(uploads);
  return (
    <label className={className ?? "slot-upload"} title={up.error ? uploadMessage(up.error) : label}>
      {up.status === "uploading" ? <Spinner /> : <HugeiconsIcon icon={Camera01Icon} size={16} />}
      <span>{up.status === "error" ? uploadMessage(up.error) : label}</span>
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
