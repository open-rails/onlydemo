import { useEffect, useImperativeHandle, useRef, useState, type DragEvent, type Ref } from "react";
import { EncodeProgress, MediaGallery } from "@openrails/contentkit-upload/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useUploadQueue, type UseUploadQueue } from "@openrails/contentkit-upload/react";
import type { FileInfo, Op, QueueItem, ReadResult } from "@openrails/contentkit-upload";
import type { Post } from "../models";
import { HugeiconsIcon } from "@hugeicons/react";
import { CropIcon, Delete02Icon, Image01Icon, ImageAdd01Icon, SquareLock02Icon, Video01Icon } from "@hugeicons/core-free-icons";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import {
  IMAGES,
  VIDEOS,
  commit,
  displayName,
  failureMessage,
  imageTypes,
  isVideo,
  limits,
  mediaXhr,
  postRef,
  readPost,
  screenFiles,
  uploadMessage,
  uploadPath,
  uploads,
  uploadsOf,
  usePostUploads,
  videoTypes,
} from "../media";
import { CropDialog } from "./crop-dialog";
import { SortableList } from "./sortable-list";
import { toast } from "sonner";
import { toastMediaError } from "../media-errors";
import { FormError } from "./states";

// What this viewer may see as a carousel or grid: every image, then every
// video, with access; otherwise the first image's blurred preview and the
// locked rest, which a purchase or membership unlocks.
export function PostGallery({ post, viewer, unlock }: { post: Post; viewer?: string; unlock: () => void }) {
  const media = useQuery({
    queryKey: ["post-media", post.id, viewer],
    queryFn: () => readPost(post.id),
  });
  const data = media.data;
  if (!data || (data.total === 0 && !data.previews?.length)) return null;
  return (
    <div className="post-gallery">
      <MediaGallery
        read={data}
        hlsBase={(dir) => uploads.hlsBase(postRef(post.id), dir)}
        xhrSetup={mediaXhr}
        refresh={() => void media.refetch()}
        renderLocked={() => (
          <Button size="sm" variant="secondary" onClick={unlock}>
            <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
            Unlock
          </Button>
        )}
        label="Post media"
      />
    </div>
  );
}

// A publishing post's status for its editors: it goes live by itself once
// every file is processed; a file that failed holds it back until removed.
export function PublishingNotice({ post }: { post: Post }) {
  const failed = post.media_readiness?.state === "failed" ? (post.media_readiness.failed ?? []) : [];
  const read = usePostUploads(post.id);
  const progress = uploadsOf(read.data).find((f) => isVideo(f.type) && f.progress)?.progress;
  if (failed.length)
    return (
      <Alert variant="destructive" className="publishing-notice">
        <AlertTitle>Not published: {failed.map(displayName).join(", ")} couldn't be processed</AlertTitle>
        <AlertDescription>Remove {failed.length === 1 ? "it" : "them"} and the post publishes by itself.</AlertDescription>
      </Alert>
    );
  return (
    <Alert className="publishing-notice">
      <AlertTitle>Publishes when processing finishes</AlertTitle>
      <AlertDescription>Only this channel's editors can see it until then.</AlertDescription>
      {progress && (
        <div className="media-encode">
          <EncodeProgress progress={progress} />
        </div>
      )}
    </Alert>
  );
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

export function PostMediaEditor({ postID }: { postID: string }) {
  const queue = useUploadQueue(uploads, newQueue(postID));
  return <MediaEditor postID={postID} queue={queue} />;
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
  onCount,
}: {
  postID: string;
  initial: File[];
  handle: Ref<DraftMediaHandle>;
  onBusy: (busy: boolean) => void;
  /** Images and videos in the draft, uploading or added. */
  onCount?: (n: number) => void;
}) {
  const queue = useUploadQueue(uploads, newQueue(postID));
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
const rendition = (read: ReadResult | undefined, up: FileInfo) =>
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
  const client = useQueryClient();
  const read = usePostUploads(postID);
  const [error, setError] = useState("");
  const [refused, setRefused] = useState<string[]>([]);
  const [cropping, setCropping] = useState<{ path: string; url: string; w: number; h: number }>();
  const refresh = () =>
    client.invalidateQueries({
      predicate: (q) => (q.queryKey[0] === "post-uploads" || q.queryKey[0] === "post-media") && q.queryKey[1] === postID,
    });
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
  const current = uploadsOf(read.data);
  // A file the worker refuses after upload is toasted when it happens.
  const known = useRef<Set<string> | null>(null);
  useEffect(() => {
    if (!read.data) return;
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
  }, [read.data, current]);
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
    uploads.editorView(postRef(postID), f.path).then(
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
            <MediaThumb video={video} url={video ? undefined : rendition(read.data, f)} />
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
      {empty && read.isSuccess && <MediaDrop empty onFiles={add} />}
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
