// Interim: the composer's draft media until MediaFolderEditor takes the
// files it was opened with (ContentKit 0.68.1).
import { useEffect, useImperativeHandle, useRef, useState, type DragEvent, type Ref } from "react";
import { EncodeProgress } from "@openrails/contentkit-ui";
import { useMediaRead, useUploadQueue, type UseUploadQueue } from "@openrails/contentkit-ui/react";
import { ContentKitError, type FileInfo, type Op, type QueueItem, type ReadResult } from "@openrails/contentkit-ui/client";
import { useMutation } from "@tanstack/react-query";
import { HugeiconsIcon } from "@hugeicons/react";
import { CropIcon, Delete02Icon, Image01Icon, ImageAdd01Icon, Video01Icon } from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { contentkit, postRef } from "../media";
import { mediaMessage, toastMediaError } from "../media-errors";
import { CropDialog } from "./crop-dialog";
import { SortableList } from "./sortable-list";
import { toast } from "sonner";
import { FormError } from "./states";

// Mirrors the post kind's uploads (media.go); the server enforces them.
const limits = {
  images: 40,
  videos: 10,
  imageBytes: 25 << 20,
  videoBytes: 20 * 1024 ** 3,
};
const imageTypes = ["image/jpeg", "image/png", "image/webp", "image/gif", "image/avif"]; // keep in sync with media.go imageTypes
const videoTypes = ["video/mp4", "video/webm", "video/quicktime", "video/x-matroska"];
const isVideo = (type?: string) => !!type?.startsWith("video/");

// A post's upload paths: ContentKit orders images and videos each in turn.
const IMAGES = "images/";
const VIDEOS = "videos/";
const uploadPath = (file: File) =>
  (isVideo(file.type) ? VIDEOS : IMAGES) + `${crypto.randomUUID().slice(0, 8)}-${file.name}`;
const displayName = (path: string) => path.replace(/^(images|videos)\//, "").replace(/^[0-9a-f]{8}-/, "");

// Browsers leave .mkv/.mov types empty or nonstandard; name them for presign.
const byExtension: Record<string, string> = {
  mkv: "video/x-matroska",
  mov: "video/quicktime",
  mp4: "video/mp4",
  m4v: "video/mp4",
  webm: "video/webm",
};
function withMediaType(file: File) {
  if (imageTypes.includes(file.type) || videoTypes.includes(file.type)) return file;
  const type = byExtension[file.name.split(".").pop()?.toLowerCase() ?? ""];
  return type ? new File([file], file.name, { type, lastModified: file.lastModified }) : file;
}

// Client-side checks of the post's uploads; the server enforces them again.
export function screenFiles(list: Iterable<File>, have: { images: number; videos: number }) {
  let { images, videos } = have;
  const accepted: File[] = [];
  const refused: string[] = [];
  for (const file of [...list].map(withMediaType)) {
    const video = isVideo(file.type);
    if (!imageTypes.includes(file.type) && !videoTypes.includes(file.type)) refused.push(`${file.name}: unsupported type.`);
    else if (file.size > (video ? limits.videoBytes : limits.imageBytes))
      refused.push(`${file.name}: over the ${video ? "20 GiB video" : "25 MiB image"} limit.`);
    else if (video && videos >= limits.videos) refused.push(`${file.name}: a post holds at most ${limits.videos} videos.`);
    else if (!video && images >= limits.images) refused.push(`${file.name}: a post holds at most ${limits.images} images.`);
    else {
      accepted.push(file);
      if (video) videos++;
      else images++;
    }
  }
  return { accepted, refused };
}

/** An editor read's uploads, images then videos. */
export const uploadsOf = (read?: ReadResult | null) => (read?.files ?? []).filter((f) => f.upload && !f.unattached);

const commit = (id: string, ops: Op[]) => contentkit.media.commit(postRef(id), ops);

function uploadMessage(error: unknown) {
  if (!(error instanceof ContentKitError)) return mediaMessage(error);
  switch (error.code) {
    case "rate_limited":
      return `Upload limit reached. Try again in ${Math.ceil((error.retryAfter ?? 60) / 60)} min.`;
    case "quota_exceeded":
      return "This channel's storage is full. Remove files from its posts to make room.";
    case "too_large":
      return "That file is too large (images 25 MiB, videos 20 GiB).";
    case "too_many_files":
      return `A post holds at most ${limits.images} images and ${limits.videos} videos.`;
    case "forbidden":
      return "You can't upload here.";
  }
  return mediaMessage(error);
}

/** Why an upload cannot be processed, in words. */
function failureMessage(f: FileInfo) {
  if (isVideo(f.type))
    return /aspect/i.test(f.failed?.message ?? "")
      ? "Its shape is not supported: videos must be between 1:2.4 (tall) and 2.4:1 (wide)."
      : "It could not be processed. Try another file or format.";
  return mediaMessage({ code: f.failed?.code ?? "internal_error", message: f.failed?.message, details: f.failed?.details, refusal: !!f.failed?.code });
}

const accept = [...imageTypes, ...videoTypes, ".mkv", ".mov"].join(",");

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
        Images up to 25 MB, videos up to 20 GB; up to {limits.images} images and {limits.videos} videos. Shapes from 1:2.4
        to 2.4:1.
      </span>
      {input}
    </label>
  );
}

const newQueue = (postID: string) => ({ ref: postRef(postID), path: IMAGES + "{name}" });

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
  onCount,
}: {
  postID: string;
  initial: File[];
  handle: Ref<DraftMediaHandle>;
  onBusy: (busy: boolean) => void;
  /** Images and videos in the draft, uploading or added. */
  onCount?: (n: number) => void;
}) {
  const queue = useUploadQueue(newQueue(postID));
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    queue.add(initial, { path: uploadPath });
  }, [queue, initial]);
  useImperativeHandle(handle, () => ({
    discard: () => {
      for (const item of queue.queue.getSnapshot().items) queue.remove(item.id);
      queue.pause();
    },
  }));
  return <MediaEditor postID={postID} queue={queue} draft onBusy={onBusy} onCount={onCount} />;
}

// The thumbnail of an upload: its image rendition, once rendered.
const rendition = (read: ReadResult | null, up: FileInfo) =>
  read?.files.find((f) => !f.upload && f.from === up.path && f.url && f.type.startsWith("image/"))?.url;

// Creator tools: upload images and videos with the ContentKit SDK (hashing,
// resumable multipart), then reorder, crop or remove them. ContentKit renders
// the images and encodes the videos; the first image, blurred, is the
// preview readers without access see.
function MediaEditor({
  postID,
  queue,
  draft,
  onBusy,
  onCount,
}: {
  postID: string;
  queue: UseUploadQueue;
  draft?: boolean;
  onBusy?: (busy: boolean) => void;
  onCount?: (n: number) => void;
}) {
  const read = useMediaRead(postRef(postID), { editor: true });
  const [error, setError] = useState("");
  const [refused, setRefused] = useState<string[]>([]);
  const [cropping, setCropping] = useState<{ path: string; url: string; w: number; h: number }>();
  const refresh = read.reload;
  const edit = useMutation({
    mutationFn: (ops: Op[]) => commit(postID, ops),
    onSuccess: refresh,
    onError: (e) => {
      setError(uploadMessage(e));
      toastMediaError(e, "Couldn't update the media");
    },
  });
  // A draft commits the uploaded head of the queue, so the post keeps the
  // order files were added in; a live post commits on "Add to post".
  const publish = useMutation({
    mutationFn: async () => {
      await queue.commit(undefined, { head: draft });
    },
    onSuccess: () => {
      for (const item of queue.queue.getSnapshot().items) if (item.status === "committed") queue.remove(item.id);
      setError("");
      return refresh();
    },
    onError: (e) => {
      setError(uploadMessage(e));
      toastMediaError(e, "Couldn't add the media to the post");
    },
  });
  const uploaded = queue.items.length > 0 && queue.items[0].status === "uploaded";
  const { mutate: commitNow, isPending: committing, isError: commitFailed } = publish;
  useEffect(() => {
    if (draft && uploaded && !committing && !commitFailed) commitNow();
  }, [draft, uploaded, committing, commitFailed, commitNow]);
  // Each failed upload and each queue-wide refusal is toasted once; the rows keep showing it.
  const toasted = useRef(new Set<string>());
  useEffect(() => {
    for (const i of queue.items)
      if (i.status === "failed" && !toasted.current.has(i.id)) {
        toasted.current.add(i.id);
        toastMediaError(i.error, `Couldn't upload ${i.file.name}`);
      }
  }, [queue.items]);
  useEffect(() => {
    if (queue.blocked) toastMediaError(queue.blocked, "Uploads paused");
  }, [queue.blocked]);
  const current = uploadsOf(read.read);
  // A file the worker refuses after upload is toasted when it happens.
  const known = useRef<Set<string> | null>(null);
  useEffect(() => {
    if (!read.read) return;
    const failed = current.filter((f) => f.failed).map((f) => ({ f, key: `${f.path}:${f.failed!.of}` }));
    if (!known.current) {
      known.current = new Set(failed.map((x) => x.key));
      return;
    }
    for (const { f, key } of failed) {
      if (known.current.has(key)) continue;
      known.current.add(key);
      toast.error(`${displayName(f.path)} couldn't be processed`, { description: failureMessage(f) });
    }
  }, [read.read, current]);
  const busy = queue.items.length > 0 || committing || edit.isPending;
  useEffect(() => onBusy?.(busy), [busy, onBusy]);

  const images = current.filter((f) => f.path.startsWith(IMAGES));
  const videos = current.filter((f) => f.path.startsWith(VIDEOS));
  const queued = (video: boolean) => queue.items.filter((i) => isVideo(i.file.type) === video).length;
  const add = (list: File[]) => {
    const { accepted, refused } = screenFiles(list, {
      images: images.length + queued(false),
      videos: videos.length + queued(true),
    });
    setRefused(refused);
    if (refused.length) toast.error(refused.length === 1 ? "A file was not added" : `${refused.length} files were not added`, { description: refused.join(" ") });
    queue.add(accepted, { path: uploadPath });
    if (queue.blocked) queue.start();
    if (commitFailed) publish.reset();
  };
  const crop = (f: FileInfo) =>
    contentkit.media.editorView(postRef(postID), f.path).then(
      (v) => setCropping({ path: f.path, url: v.url, w: v.width, h: v.height }),
      (e: unknown) => toastMediaError(e, "slot.load"),
    );
  const count = current.length + queue.items.length;
  useEffect(() => onCount?.(count), [count, onCount]);
  const empty = count === 0;
  const group = (files: FileInfo[]) => (
    <SortableList
      className="media-list"
      items={files}
      id={(f) => f.path}
      name={(f) => displayName(f.path)}
      onMove={(from, to) => edit.mutate([{ op: "move", path: files[from].path, index: to }])}
    >
      {(f, handle) => {
        const video = isVideo(f.type);
        const processing = !f.failed && (f.staged || (f.pending?.length ?? 0) > 0);
        return (
          <>
            {handle}
            <MediaThumb video={video} url={video ? undefined : rendition(read.read, f)} />
            <span className="media-name" title={f.path}>
              {displayName(f.path)}
            </span>
            {f.edit && <Badge variant="outline">Edited</Badge>}
            {video && processing && (
              <div className="media-encode">
                <EncodeProgress progress={f.progress} />
              </div>
            )}
            {f.failed && (
              <Badge variant="destructive" title={failureMessage(f)}>
                {video ? "Can't play" : "Not processed"}
              </Badge>
            )}
            <span className="inline-actions">
              {!video && (
                <Button size="icon-sm" variant="ghost" aria-label="Crop and rotate" disabled={!!f.staged} onClick={() => void crop(f)}>
                  <HugeiconsIcon icon={CropIcon} />
                </Button>
              )}
              <Button size="icon-sm" variant="ghost" aria-label="Remove" disabled={edit.isPending} onClick={() => edit.mutate([{ op: "remove", path: f.path }])}>
                <HugeiconsIcon icon={Delete02Icon} />
              </Button>
            </span>
          </>
        );
      }}
    </SortableList>
  );
  return (
    <section className={draft ? "media-editor in-composer" : "media-editor"}>
      <header>
        <h3>
          Media{" "}
          <span className="muted text-sm">
            {images.length} images, {videos.length} videos
          </span>
        </h3>
        {!empty && <MediaDrop onFiles={add} />}
      </header>
      {empty && !!read.read && <MediaDrop empty onFiles={add} />}
      <div className="grid gap-1">
        {group(images)}
        {group(videos)}
        <SortableList
          className="media-list"
          items={queue.items}
          id={(item) => item.id}
          name={(item) => item.file.name}
          disabled={draft}
          row={(item) => ({ className: "queued", "data-failed": item.status === "failed" || undefined })}
          onMove={(from, to) => queue.move(queue.items[from].id, to)}
        >
          {(item, handle) => (
            <>
              {!draft && handle}
              <MediaThumb video={isVideo(item.file.type)} />
              <span className="media-name" title={item.file.name}>
                {item.file.name}
              </span>
              {item.status === "uploading" && item.progress && <progress max={Math.max(1, item.progress.total)} value={item.progress.loaded} />}
              {item.status === "uploaded" && item.unattached && !item.processed && item.processing?.progress && (
                <progress max={100} value={item.processing.progress.percent} />
              )}
              <span className={item.status === "failed" ? "text-sm text-destructive" : "muted text-sm"}>
                {item.status === "uploading" && item.progress
                  ? `${item.progress.phase} ${Math.round((100 * item.progress.loaded) / Math.max(1, item.progress.total))}%`
                  : item.status === "failed"
                    ? uploadMessage(item.error)
                    : item.status === "uploaded" && draft
                      ? "adding…"
                      : item.status === "uploaded" && item.unattached
                        ? processingLabel(item)
                        : item.status}
              </span>
              <span className="inline-actions">
                {item.status === "failed" && (
                  <Button size="sm" variant="ghost" onClick={() => queue.retry(item.id)}>
                    Retry
                  </Button>
                )}
                <Button size="icon-sm" variant="ghost" aria-label="Remove" onClick={() => queue.remove(item.id)}>
                  <HugeiconsIcon icon={Delete02Icon} />
                </Button>
              </span>
            </>
          )}
        </SortableList>
      </div>
      {cropping && (
        <CropDialog
          title="Crop and rotate"
          src={cropping.url}
          dims={{ w: cropping.w, h: cropping.h }}
          initial={current.find((f) => f.path === cropping.path)?.edit}
          onSave={(e) => commit(postID, [{ op: "edit", path: cropping.path, ...(e ? { edit: e } : {}) }]).then(refresh)}
          onClose={() => setCropping(undefined)}
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
      {(!draft || commitFailed) && queue.items.some((i) => i.status === "uploaded") && (
        <Button disabled={!queue.ready || committing} onClick={() => publish.mutate()}>
          {committing && <Spinner data-icon="inline-start" />}
          {commitFailed ? "Try adding again" : `Add ${queue.items.length} to post`}
        </Button>
      )}
      {images.length > 0 && <p className="muted text-sm">Readers without access see the first image, blurred.</p>}
    </section>
  );
}

function MediaThumb({ video, url }: { video: boolean; url?: string }) {
  return (
    <span className="media-thumb" data-video={video || undefined}>
      {url ? <img src={url} alt="" loading="lazy" /> : <HugeiconsIcon icon={video ? Video01Icon : Image01Icon} size={18} />}
    </span>
  );
}

// processingLabel is a queue row's state while the media worker processes the
// file before "Add to post".
function processingLabel(item: QueueItem): string {
  const p = item.processing;
  if (p?.failed) return `can't process: ${failureMessage(p)}`;
  if (item.processed) return "processed";
  if (p?.progress && p.progress.phase !== "queued") return `processing ${Math.round(p.progress.percent)}%`;
  return "processing…";
}
