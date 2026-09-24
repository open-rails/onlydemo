import { useEffect, useImperativeHandle, useRef, useState, type DragEvent, type Ref } from "react";
import { ImageCropDialog, useMessages } from "@openrails/contentkit-upload/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useUploadQueue, type UseUploadQueue } from "@openrails/contentkit-upload/react";
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
  screenFiles,
  postRef,
  readPost,
  uploadMessage,
  uploads,
  videoTypes,
  useSlotSaved,
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
// resumable multipart, reorder before commit), then reorder, remove, crop or
// pick an image as the teaser. ContentKit derives image variants and HLS.
// Channel slots a post image can fill (managers only), cropped at the slot's aspect.
const channelSlots = {
  avatar: { aspect: 1, target: 512, label: "Use as channel avatar", short: "Avatar" },
  cover: { aspect: 3, target: 3000, label: "Use as channel cover", short: "Cover" },
};
type ChannelSlot = keyof typeof channelSlots;
const accept = [...imageTypes, ...videoTypes, ".mkv", ".mov"].join(",");

const uniqueName = (f: File) => `${crypto.randomUUID().slice(0, 8)}-${f.name}`;
const displayName = (name: string) => name.replace(/^[0-9a-f]{8}-/, "");

// A file picker that also takes drops; `empty` renders it as the big drop zone.
export function MediaDrop({
  onFiles,
  empty,
  disabled,
}: {
  onFiles: (files: File[]) => void;
  empty?: boolean;
  disabled?: boolean;
}) {
  const [over, setOver] = useState(false);
  const input = (
    <input
      type="file"
      accept={accept}
      multiple
      className="sr-only"
      disabled={disabled}
      onChange={(e) => {
        onFiles([...(e.target.files || [])]);
        e.target.value = "";
      }}
    />
  );
  const drop = {
    onDragOver: (e: DragEvent) => {
      if (disabled || !e.dataTransfer.types.includes("Files")) return;
      e.preventDefault();
      setOver(true);
    },
    onDragLeave: () => setOver(false),
    onDrop: (e: DragEvent) => {
      if (disabled) return;
      e.preventDefault();
      setOver(false);
      onFiles([...e.dataTransfer.files]);
    },
  };
  if (!empty)
    return (
      <label className="upload-button" data-over={over || undefined} {...drop}>
        <HugeiconsIcon icon={ImageAdd01Icon} size={18} />
        Add images or videos
        {input}
      </label>
    );
  return (
    <label className="media-drop" data-over={over || undefined} data-disabled={disabled || undefined} {...drop}>
      <HugeiconsIcon icon={ImageAdd01Icon} size={28} />
      <strong>Drop images or videos, or click to choose</strong>
      <span className="muted text-sm">
        Images up to 25 MB, videos up to 20 GB; up to {limits.videos} videos and {limits.files} files. Shapes from 1:2.4 to
        2.4:1.
      </span>
      {input}
    </label>
  );
}

export function PostMediaEditor({ postID, channel }: { postID: number; channel?: string }) {
  const queue = useUploadQueue(uploads, { ref: postRef(postID) });
  return <MediaEditor postID={postID} channel={channel} queue={queue} />;
}

export interface DraftMediaHandle {
  /** Stops uploads and aborts their multipart uploads before the draft is deleted. */
  discard: () => void;
}

// The composer's media: a draft post's folder, committed as files finish.
export function DraftMediaEditor({
  postID,
  initial,
  handle,
  onBusy,
}: {
  postID: number;
  initial: File[];
  handle: Ref<DraftMediaHandle>;
  onBusy: (busy: boolean) => void;
}) {
  const queue = useUploadQueue(uploads, { ref: postRef(postID) });
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    queue.add(initial, { name: uniqueName });
  }, [queue, initial]);
  useImperativeHandle(handle, () => ({
    discard: () => {
      for (const item of queue.queue.getSnapshot().items) queue.remove(item.id);
      queue.pause();
    },
  }));
  return <MediaEditor postID={postID} queue={queue} draft onBusy={onBusy} />;
}

function MediaEditor({
  postID,
  channel,
  queue,
  draft,
  onBusy,
}: {
  postID: number;
  channel?: string;
  queue: UseUploadQueue;
  draft?: boolean;
  onBusy?: (busy: boolean) => void;
}) {
  const client = useQueryClient();
  const files = useQuery({
    queryKey: ["post-files", postID],
    queryFn: () => postFiles(postID),
  });
  const [error, setError] = useState("");
  const [refused, setRefused] = useState<string[]>([]);
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
  // A draft commits the uploaded head of the queue, so the post keeps the order files were added in.
  const commitHead = async () => {
    const items = queue.queue.getSnapshot().items;
    const end = items.findIndex((i) => i.status !== "uploaded" && i.status !== "committed");
    const head = (end < 0 ? items : items.slice(0, end)).filter((i) => i.status === "uploaded");
    if (head.length === 0) return;
    await uploads.commit(
      postRef(postID),
      head.map((i) => ({ op: "insert" as const, name: i.name, original: i.result!.name, meta: i.meta })),
      { sources: Object.fromEntries(head.map((i) => [i.result!.name, { file: i.file, type: i.result!.type }])) },
    );
    for (const i of head) queue.remove(i.id);
  };
  const publish = useMutation({
    mutationFn: async () => {
      await (draft ? commitHead() : queue.commit());
    },
    onSuccess: () => {
      for (const item of queue.queue.getSnapshot().items) if (item.status === "committed") queue.remove(item.id);
      setError("");
      return refresh();
    },
    onError: (e) => setError(uploadMessage(e)),
  });
  // A draft commits each file as it finishes, so it can be cropped or picked as
  // the teaser before publishing; a live post commits on "Add to post".
  const uploaded = queue.items.length > 0 && queue.items[0].status === "uploaded";
  const { mutate: commitNow, isPending: committing, isError: commitFailed } = publish;
  useEffect(() => {
    if (draft && uploaded && !committing && !commitFailed) commitNow();
  }, [draft, uploaded, committing, commitFailed, commitNow]);
  const busy = queue.items.length > 0 || committing || edit.isPending;
  useEffect(() => onBusy?.(busy), [busy, onBusy]);

  const current = files.data?.files || [];
  const images = current.filter((f) => f.name !== TEASER);
  const add = (list: File[]) => {
    const { accepted, refused } = screenFiles(list, {
      files: images.length + queue.items.length,
      videos:
        images.filter((f) => isVideo(f.type)).length + queue.items.filter((i) => isVideo(i.file.type)).length,
    });
    setRefused(refused);
    queue.add(accepted, { name: uniqueName });
    if (queue.blocked) queue.start();
    if (commitFailed) publish.reset();
  };
  const teaser = current.find((f) => f.name === TEASER);
  const setTeaser = (original: string) =>
    edit.mutate([
      ...(teaser ? [{ op: "remove" as const, name: TEASER }] : []),
      { op: "insert", name: TEASER, original, index: 0, meta: { teaser: true } },
    ]);
  const index = (name: string) => current.findIndex((f) => f.name === name);
  const empty = images.length === 0 && queue.items.length === 0;
  return (
    <section className={draft ? "media-editor in-composer" : "media-editor"}>
      <header>
        <h3>
          Media{" "}
          <span className="muted text-sm">
            {images.length + queue.items.length}/{limits.files}
          </span>
        </h3>
        {!empty && <MediaDrop onFiles={add} />}
      </header>
      {empty && files.isSuccess && <MediaDrop empty onFiles={add} />}
      <ol className="media-list">
        {images.map((f, i) => {
          const e = editable.get(f.name);
          const video = isVideo(f.type);
          return (
            <li key={f.name}>
              <MediaThumb video={video} url={e?.url} />
              <span className="media-name" title={f.name}>
                {displayName(f.name)}
              </span>
              {f.edit && <Badge variant="outline">Edited</Badge>}
              {video && e && !e.hls && !e.failed && (
                <Badge variant="outline">
                  <Spinner data-icon="inline-start" />
                  Processing video…
                </Badge>
              )}
              {e?.failed && (
                <Badge variant="destructive" title={failureMessage(e.failed)}>
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
                  onClick={() => edit.mutate([{ op: "move", name: f.name, index: index(images[i - 1].name) }])}
                >
                  <HugeiconsIcon icon={ArrowUp01Icon} />
                </Button>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label="Move down"
                  disabled={i === images.length - 1 || edit.isPending}
                  onClick={() => edit.mutate([{ op: "move", name: f.name, index: index(images[i + 1].name) }])}
                >
                  <HugeiconsIcon icon={ArrowDown01Icon} />
                </Button>
                {!video && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label="Crop and rotate"
                    disabled={!e?.dims}
                    onClick={() => setCropping({ name: f.name })}
                  >
                    <HugeiconsIcon icon={CropIcon} />
                  </Button>
                )}
                {channel &&
                  !video &&
                  (Object.keys(channelSlots) as ChannelSlot[]).map((slot) => (
                    <Button
                      key={slot}
                      size="sm"
                      variant="ghost"
                      title={channelSlots[slot].label}
                      aria-label={channelSlots[slot].label}
                      disabled={!e?.dims}
                      onClick={() => setCropping({ name: f.name, slot })}
                    >
                      {channelSlots[slot].short}
                    </Button>
                  ))}
                {!video && (
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
                      ...(teaser?.original === f.original ? [{ op: "remove" as const, name: TEASER }] : []),
                    ])
                  }
                >
                  <HugeiconsIcon icon={Delete02Icon} />
                </Button>
              </span>
            </li>
          );
        })}
        {queue.items.map((item, i) => (
          <li key={item.id} className="queued" data-failed={item.status === "failed" || undefined}>
            <MediaThumb video={isVideo(item.file.type)} />
            <span className="media-name" title={item.file.name}>
              {item.file.name}
            </span>
            {item.status === "uploading" && item.progress && (
              <progress max={Math.max(1, item.progress.total)} value={item.progress.loaded} />
            )}
            <span className={item.status === "failed" ? "text-sm text-destructive" : "muted text-sm"}>
              {item.status === "uploading" && item.progress
                ? `${item.progress.phase} ${Math.round((100 * item.progress.loaded) / Math.max(1, item.progress.total))}%`
                : item.status === "failed"
                  ? uploadMessage(item.error)
                  : item.status === "uploaded" && draft
                    ? "adding…"
                    : item.status}
            </span>
            <span className="inline-actions">
              {!draft && (
                <>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label="Move up"
                    disabled={i === 0}
                    onClick={() => queue.move(item.id, i - 1)}
                  >
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
                </>
              )}
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
      {refused.length > 0 && (
        <ul className="media-refused" role="alert">
          {refused.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      )}
      <FormError>{error}</FormError>
      {notice && <p className="muted text-sm">{notice}</p>}
      {(!draft || commitFailed) && queue.items.some((i) => i.status === "uploaded") && (
        <Button disabled={!queue.ready || committing} onClick={() => publish.mutate()}>
          {committing && <Spinner data-icon="inline-start" />}
          {commitFailed ? "Try adding again" : `Add ${queue.items.length} to post`}
        </Button>
      )}
      {!teaser && images.some((f) => !isVideo(f.type)) && (
        <p className="muted text-sm">Pick an image as the teaser: readers without access see it blurred.</p>
      )}
    </section>
  );
}

function MediaThumb({ video, url }: { video: boolean; url?: string }) {
  return (
    <span className="media-thumb">
      {url && !video ? (
        <img src={url} alt="" loading="lazy" />
      ) : (
        <HugeiconsIcon icon={video ? Video01Icon : Image01Icon} size={18} />
      )}
    </span>
  );
}
