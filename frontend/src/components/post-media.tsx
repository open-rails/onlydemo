import { useState } from "react";
import { ImageCropDialog, useMessages } from "@openrails/contentkit-upload/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useUploadQueue } from "@openrails/contentkit-upload/react";
import type { Op } from "@openrails/contentkit-upload";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  AlertCircleIcon,
  ArrowDown01Icon,
  ArrowUp01Icon,
  BlurIcon,
  CropIcon,
  Delete02Icon,
  Download04Icon,
  Image01Icon,
  ImageAdd01Icon,
  SquareLock02Icon,
  Video01Icon,
} from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import {
  channelRef,
  commit,
  hlsBase,
  imageTypes,
  isVideo,
  limits,
  postFiles,
  postRef,
  readPost,
  uploadMessage,
  uploads,
  videoTypes,
  useSlotSaved,
  withMediaType,
  type MediaDownload,
  type MediaFile,
} from "../media";
import { CropDialog } from "./crop-dialog";
import { FormError } from "./states";
import { VideoPlayer } from "./video-player";

const TEASER = "teaser";
// The editor variant is a downscaled whole source; crops stay in its original pixels.
const slotSource = (f?: MediaFile) =>
  f?.url && f.dims ? { url: f.url, width: f.dims.w, height: f.dims.h } : null;
const ready = (f: MediaFile) => (isVideo(f.type) ? !!f.hls || !!f.failed : !!f.url);
const pending = (files?: MediaFile[]) =>
  !!files?.some((f) => !f.locked && !ready(f));
const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

function lockedLabel(files: MediaFile[]) {
  const videos = files.filter((f) => isVideo(f.type)).length;
  const label = plural(files.length, "item");
  return videos ? `${label} locked (${plural(videos, "video")})` : `${label} locked`;
}

// Why a video cannot be encoded, for its editors (ContentKit's hls.error).
function failureMessage(reason: string) {
  return /aspect/i.test(reason)
    ? "Its shape is not supported: videos must be between 1:2.4 (tall) and 2.4:1 (wide)."
    : "It could not be processed. Try another file or format.";
}

// Per-quality downloads of one video: keys are "{file}-{rung}p", the rung
// being the short side (a vertical 1080p is 1080 wide).
function Downloads({ name, downloads }: { name: string; downloads: MediaDownload[] }) {
  const mine = downloads
    .filter((d) => d.key.startsWith(`${name}-`))
    .sort((a, b) => parseInt(b.key.slice(name.length + 1)) - parseInt(a.key.slice(name.length + 1)));
  if (mine.length === 0) return null;
  return (
    <p className="video-downloads">
      <HugeiconsIcon icon={Download04Icon} size={16} />
      {mine.map((d) => (
        <a key={d.key} href={d.url} download={d.name} title={d.name}>
          {d.key.slice(name.length + 1)}
        </a>
      ))}
    </p>
  );
}

// What this viewer may see, in manifest order: every image and video with
// full access, otherwise the blurred teaser and a count of what a purchase or
// membership unlocks.
export function PostGallery({ postID, viewer }: { postID: number; viewer?: string }) {
  const media = useQuery({
    queryKey: ["post-media", postID, viewer],
    queryFn: () => readPost(postID, "large,blurred"),
    refetchInterval: (q) => (pending(q.state.data?.files) ? 3000 : false),
  });
  const data = media.data;
  if (!data || data.total === 0) return null;
  const full = data.access === "full";
  const shown = data.files.filter((f) => !f.locked && (full ? !f.teaser : true));
  const locked = data.files.filter((f) => f.locked);
  return (
    <div className="post-gallery">
      {shown.map((f) => {
        if (isVideo(f.type)) {
          if (f.failed)
            return (
              <figure key={f.index} className="video-failed" role="alert">
                <HugeiconsIcon icon={AlertCircleIcon} size={20} />
                <span>
                  <strong>{f.name}</strong> can't be played. {failureMessage(f.failed)}
                </span>
              </figure>
            );
          if (!f.hls || !f.name)
            return (
              <figure key={f.index} className="video-pending">
                <Spinner /> Processing video…
              </figure>
            );
          return (
            <figure key={f.index}>
              <VideoPlayer base={hlsBase(postID, f.name)} width={f.w} height={f.h} />
              {full && <Downloads name={f.name} downloads={data.downloads ?? []} />}
            </figure>
          );
        }
        if (!f.url) return null;
        return (
          <figure key={f.index} className={f.teaser && !full ? "teaser" : undefined}>
            <img src={f.url} alt={f.name || ""} loading="lazy" />
            {f.teaser && !full && locked.length > 0 && (
              <figcaption>
                <HugeiconsIcon icon={SquareLock02Icon} size={22} />
                {lockedLabel(locked)}
              </figcaption>
            )}
          </figure>
        );
      })}
      {!shown.some((f) => f.teaser) && locked.length > 0 && (
        <p className="muted text-sm locked-note">
          <HugeiconsIcon icon={SquareLock02Icon} size={16} /> {lockedLabel(locked)}
        </p>
      )}
      {shown.some((f) => !isVideo(f.type) && !f.url) && (
        <p className="muted text-sm">
          <Spinner className="inline" /> Processing images…
        </p>
      )}
    </div>
  );
}

// Creator tools: upload images and videos with the ContentKit SDK (hashing,
// resumable multipart, reorder before commit), then reorder, remove or pick
// an image as the teaser. ContentKit derives image variants and HLS.
// Channel slots a post image can fill (managers only), cropped at the slot's aspect.
const channelSlots = {
  avatar: { aspect: 1, target: 512, label: "Use as channel avatar", short: "Avatar" },
  cover: { aspect: 3, target: 3000, label: "Use as channel cover", short: "Cover" },
};
type ChannelSlot = keyof typeof channelSlots;

export function PostMediaEditor({ postID, channel }: { postID: number; channel?: string }) {
  const client = useQueryClient();
  const files = useQuery({
    queryKey: ["post-files", postID],
    queryFn: () => postFiles(postID),
  });
  const queue = useUploadQueue(uploads, { ref: postRef(postID) });
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [cropping, setCropping] = useState<{ name: string; slot?: ChannelSlot }>();
  const [slotSaving, setSlotSaving] = useState(false);
  const [slotError, setSlotError] = useState("");
  const saved = useSlotSaved();
  const messages = useMessages();
  const slotCrop = cropping?.slot && channel ? { name: cropping.name, slot: cropping.slot, ref: channelRef(channel) } : undefined;
  // The unedited "editor" variant, source dims and current edit per image.
  const editor = useQuery({
    queryKey: ["post-media-editor", postID],
    queryFn: () => readPost(postID, "editor"),
    refetchInterval: (q) =>
      q.state.data?.files.some((f) => (isVideo(f.type) ? !f.hls && !f.failed : !f.url || !f.dims)) ? 3000 : false,
  });
  const editable = new Map((editor.data?.files ?? []).map((f) => [f.name, f]));
  const refresh = () =>
    client.invalidateQueries({
      predicate: (q) =>
        typeof q.queryKey[0] === "string" &&
        (q.queryKey[0].startsWith("post-media") || q.queryKey[0] === "post-files") &&
        q.queryKey[1] === postID,
    });
  const edit = useMutation({
    mutationFn: (ops: Op[]) => commit(postID, ops),
    onSuccess: refresh,
    onError: (e) => setError(uploadMessage(e)),
  });
  const publish = useMutation({
    mutationFn: () => queue.commit(),
    onSuccess: () => {
      for (const item of queue.items) queue.remove(item.id);
      return refresh();
    },
    onError: (e) => setError(uploadMessage(e)),
  });
  const current = files.data?.files || [];
  const images = current.filter((f) => f.name !== TEASER);
  const add = (list: FileList | null) => {
    let count = images.length + queue.items.length;
    let videos =
      images.filter((f) => isVideo(f.type)).length +
      queue.items.filter((i) => isVideo(i.file.type)).length;
    const accepted: File[] = [];
    const refused: string[] = [];
    for (const file of [...(list || [])].map(withMediaType)) {
      const video = isVideo(file.type);
      if (!imageTypes.includes(file.type) && !videoTypes.includes(file.type))
        refused.push(`${file.name}: unsupported type.`);
      else if (file.size > (video ? limits.videoBytes : limits.imageBytes))
        refused.push(`${file.name}: over the ${video ? "20 GiB video" : "25 MiB image"} limit.`);
      else if (count >= limits.files)
        refused.push(`${file.name}: a post holds at most ${limits.files} files.`);
      else if (video && videos >= limits.videos)
        refused.push(`${file.name}: a post holds at most ${limits.videos} videos.`);
      else {
        accepted.push(file);
        count++;
        if (video) videos++;
      }
    }
    setError(refused.join(" "));
    queue.add(accepted, {
      name: (f) => `${crypto.randomUUID().slice(0, 8)}-${f.name}`,
    });
    if (queue.blocked) queue.start();
  };
  const teaser = current.find((f) => f.name === TEASER);
  const setTeaser = (original: string) =>
    edit.mutate([
      ...(teaser ? [{ op: "remove" as const, name: TEASER }] : []),
      { op: "insert", name: TEASER, original, index: 0, meta: { teaser: true } },
    ]);
  const index = (name: string) => current.findIndex((f) => f.name === name);
  return (
    <section className="media-editor">
      <header>
        <h3>
          Media{" "}
          <span className="muted text-sm">
            {images.length}/{limits.files}
          </span>
        </h3>
        <label className="upload-button">
          <HugeiconsIcon icon={ImageAdd01Icon} size={18} />
          Add images or videos
          <input
            type="file"
            accept={[...imageTypes, ...videoTypes, ".mkv", ".mov"].join(",")}
            multiple
            hidden
            onChange={(e) => {
              add(e.target.files);
              e.target.value = "";
            }}
          />
        </label>
      </header>
      <ol className="media-list">
        {images.map((f, i) => (
          <li key={f.name}>
            <HugeiconsIcon icon={isVideo(f.type) ? Video01Icon : Image01Icon} size={16} />
            <span className="media-name">{f.name}</span>
            {f.edit && <Badge variant="outline">Edited</Badge>}
            {editable.get(f.name)?.failed && (
              <Badge variant="destructive" title={failureMessage(editable.get(f.name)!.failed!)}>
                Can't play
              </Badge>
            )}
            {teaser?.original === f.original && (
              <Badge variant="secondary">
                <HugeiconsIcon icon={BlurIcon} data-icon="inline-start" />
                Teaser
              </Badge>
            )}
            <span className="inline-actions">
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="Move up"
                disabled={i === 0 || edit.isPending}
                onClick={() =>
                  edit.mutate([{ op: "move", name: f.name, index: index(images[i - 1].name) }])
                }
              >
                <HugeiconsIcon icon={ArrowUp01Icon} />
              </Button>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="Move down"
                disabled={i === images.length - 1 || edit.isPending}
                onClick={() =>
                  edit.mutate([{ op: "move", name: f.name, index: index(images[i + 1].name) }])
                }
              >
                <HugeiconsIcon icon={ArrowDown01Icon} />
              </Button>
              {!isVideo(f.type) && (
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label="Crop and rotate"
                  disabled={!editable.get(f.name)?.dims}
                  onClick={() => setCropping({ name: f.name })}
                >
                  <HugeiconsIcon icon={CropIcon} />
                </Button>
              )}
              {channel &&
                !isVideo(f.type) &&
                (Object.keys(channelSlots) as ChannelSlot[]).map((slot) => (
                  <Button
                    key={slot}
                    size="sm"
                    variant="ghost"
                    title={channelSlots[slot].label}
                    aria-label={channelSlots[slot].label}
                    disabled={!editable.get(f.name)?.dims}
                    onClick={() => setCropping({ name: f.name, slot })}
                  >
                    {channelSlots[slot].short}
                  </Button>
                ))}
              {!isVideo(f.type) && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={teaser?.original === f.original || edit.isPending}
                  onClick={() => setTeaser(f.original)}
                >
                  Use as teaser
                </Button>
              )}
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="Remove"
                disabled={edit.isPending}
                onClick={() =>
                  edit.mutate([
                    { op: "remove", name: f.name },
                    ...(teaser?.original === f.original
                      ? [{ op: "remove" as const, name: TEASER }]
                      : []),
                  ])
                }
              >
                <HugeiconsIcon icon={Delete02Icon} />
              </Button>
            </span>
          </li>
        ))}
        {queue.items.map((item, i) => (
          <li key={item.id} className="queued">
            <HugeiconsIcon icon={isVideo(item.file.type) ? Video01Icon : Image01Icon} size={16} />
            <span className="media-name">{item.file.name}</span>
            {item.status === "uploading" && item.progress && (
              <progress max={Math.max(1, item.progress.total)} value={item.progress.loaded} />
            )}
            <span className="muted text-sm">
              {item.status === "uploading" && item.progress
                ? `${item.progress.phase} ${Math.round((100 * item.progress.loaded) / Math.max(1, item.progress.total))}%`
                : item.status === "failed"
                  ? uploadMessage(item.error)
                  : item.status}
            </span>
            <span className="inline-actions">
              <Button size="icon-sm" variant="ghost" aria-label="Move up" disabled={i === 0} onClick={() => queue.move(item.id, i - 1)}>
                <HugeiconsIcon icon={ArrowUp01Icon} />
              </Button>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="Move down"
                disabled={i === queue.items.length - 1}
                onClick={() => queue.move(item.id, i + 1)}
              >
                <HugeiconsIcon icon={ArrowDown01Icon} />
              </Button>
              {item.status === "failed" && (
                <Button size="sm" variant="ghost" onClick={() => queue.retry(item.id)}>
                  Retry
                </Button>
              )}
              <Button size="icon-sm" variant="ghost" aria-label="Remove" onClick={() => queue.remove(item.id)}>
                <HugeiconsIcon icon={Delete02Icon} />
              </Button>
            </span>
          </li>
        ))}
      </ol>
      {cropping && !cropping.slot && (
        <CropDialog
          title="Crop and rotate"
          src={editable.get(cropping.name)?.url}
          dims={editable.get(cropping.name)?.dims}
          initial={editable.get(cropping.name)?.edit}
          onSave={(e) => uploads.edit(postRef(postID), cropping.name, e).then(refresh)}
          onClose={() => setCropping(undefined)}
        />
      )}
      {slotCrop && (
        <ImageCropDialog
          open
          onOpenChange={(open) => {
            if (!open && !slotSaving) {
              setCropping(undefined);
              setSlotError("");
            }
          }}
          source={slotSource(editable.get(slotCrop.name))}
          aspect={channelSlots[slotCrop.slot].aspect}
          round={slotCrop.slot === "avatar"}
          targetWidth={channelSlots[slotCrop.slot].target}
          title={channelSlots[slotCrop.slot].label}
          busy={slotSaving}
          rendering={slotSaving}
          error={slotError || undefined}
          onConfirm={(e) => {
            const { slot, name, ref } = slotCrop;
            setSlotSaving(true);
            setSlotError("");
            uploads
              .setSlotFromFile(ref, slot, name, e ?? {}, { from: postRef(postID) })
              .then((m) => (m.pending ? uploads.waitForSlot(ref, slot) : m))
              .then((m) => {
                if (m.error) throw new Error(m.error);
                saved(ref, slot, m);
                setNotice(`Channel ${slot} updated.`);
                setCropping(undefined);
              })
              .catch((err: unknown) => setSlotError(messages.error(err)))
              .finally(() => setSlotSaving(false));
          }}
        />
      )}
      {queue.blocked && <FormError>{uploadMessage(queue.blocked)}</FormError>}
      <FormError>{error}</FormError>
      {notice && <p className="muted text-sm">{notice}</p>}
      {queue.items.length > 0 && (
        <Button
          disabled={!queue.ready || publish.isPending}
          onClick={() => publish.mutate()}
        >
          {publish.isPending && <Spinner data-icon="inline-start" />}
          Add {queue.items.length} to post
        </Button>
      )}
      {!teaser && images.some((f) => !isVideo(f.type)) && (
        <p className="muted text-sm">
          Pick an image as the teaser: readers without access see it blurred.
        </p>
      )}
    </section>
  );
}
