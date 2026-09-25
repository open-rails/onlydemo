import { useEffect, useImperativeHandle, useRef, useState, type DragEvent, type Ref } from "react";
import { EncodeProgress, HoverPreviewPicker, ImageCropDialog, MediaGallery, VideoPosterPicker, useMessages } from "@openrails/contentkit-upload/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useUploadQueue, type UseUploadQueue } from "@openrails/contentkit-upload/react";
import type { Op, QueueItem } from "@openrails/contentkit-upload";
import type { Post } from "../models";
import { HugeiconsIcon } from "@hugeicons/react";
import {
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
  readVideoImages,
  readVideoProgress,
  mediaXhr,
  uploadMessage,
  uploads,
  videoTypes,
  useSlot,
  useSlotSaved,
  type MediaDownload,
  type MediaFile,
} from "../media";
import { CropDialog } from "./crop-dialog";
import { SortableList } from "./sortable-list";
import { UploadError, slotError as slotFailure } from "@openrails/contentkit-upload";
import { toast } from "sonner";
import { mediaMessage, toastMediaError } from "../media-errors";
import { FormError } from "./states";

const TEASER = "teaser";
// The editor variant is a downscaled whole source; crops stay in its original pixels.
const slotSource = (f?: MediaFile) =>
  f?.url && f.dims ? { url: f.url, width: f.dims.w, height: f.dims.h } : null;
const ready = (f: MediaFile) => (isVideo(f.type) ? !!f.hls : !!f.url) || !!f.failed;
const pending = (files?: MediaFile[]) =>
  !!files?.some((f) => !f.locked && !ready(f));
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

// What this viewer may see, in manifest order, as a carousel or grid: every
// image and video with full access, otherwise the blurred teaser and what a
// purchase or membership unlocks. Videos show the post's cover.
export function PostGallery({ post, viewer, unlock }: { post: Post; viewer?: string; unlock: () => void }) {
  const postID = post.id;
  const media = useQuery({
    queryKey: ["post-media", postID, viewer],
    queryFn: () => readPost(postID, "large,blurred"),
    refetchInterval: (q) => (pending(q.state.data?.files) ? 3000 : false),
  });
  const art = useQuery({
    queryKey: ["post-media-art", postID, viewer],
    queryFn: () => readVideoImages(postID),
    enabled: !!media.data?.files.some((f) => isVideo(f.type)),
    retry: false,
  });
  const data = media.data;
  if (!data || data.total === 0) return null;
  const downloads = data.downloads ?? [];
  return (
    <div className="post-gallery">
      <MediaGallery
        read={data}
        hlsBase={(f) => hlsBase(postID, f.name!)}
        xhrSetup={mediaXhr}
        refresh={() => media.refetch()}
        videoImages={art.data}
        renderLocked={() => (
          <Button size="sm" variant="secondary" onClick={unlock}>
            <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
            Unlock
          </Button>
        )}
        renderDetails={(item) =>
          item.kind === "video" && item.file.name && data.access === "full" ? (
            <Downloads name={item.file.name} downloads={downloads} />
          ) : null
        }
        label="Post media"
      />
    </div>
  );
}

// Creator tools: upload images and videos with the ContentKit SDK (hashing,
// resumable multipart, reorder before commit), then reorder, remove, crop or
// pick an image as the teaser. ContentKit derives image variants and HLS.
// Channel slots a post image can fill (managers only), cropped at the slot's aspect.
const channelSlots = {
  avatar: { aspect: "1:1", target: 512, label: "Use as channel avatar", short: "Avatar" },
  cover: { aspect: "3:1", target: 3000, label: "Use as channel cover", short: "Cover" },
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

export function PostMediaEditor({ postID, channel }: { postID: string; channel?: string }) {
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
  onCount,
}: {
  postID: string;
  initial: File[];
  handle: Ref<DraftMediaHandle>;
  onBusy: (busy: boolean) => void;
  /** Images and videos in the draft, uploading or added. */
  onCount?: (n: number) => void;
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
  return <MediaEditor postID={postID} queue={queue} draft onBusy={onBusy} onCount={onCount} />;
}

function MediaEditor({
  postID,
  channel,
  queue,
  draft,
  onBusy,
  onCount,
}: {
  postID: string;
  channel?: string;
  queue: UseUploadQueue;
  draft?: boolean;
  onBusy?: (busy: boolean) => void;
  onCount?: (n: number) => void;
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
  const [picking, setPicking] = useState<{ file: string; what: "poster" | "preview" }>();
  const [slotSaving, setSlotSaving] = useState(false);
  const [slotError, setSlotError] = useState("");
  const saved = useSlotSaved();
  const messages = useMessages();
  const slotCrop = cropping?.slot && channel ? { name: cropping.name, slot: cropping.slot, ref: channelRef(channel) } : undefined;
  const slotSpec = useSlot(slotCrop?.ref ?? channelRef(""), slotCrop?.slot ?? "", null, !!slotCrop);
  // The unedited "editor" variant, source dims and current edit per image.
  const editor = useQuery({
    queryKey: ["post-media-editor", postID],
    queryFn: () => readPost(postID, "editor"),
    refetchInterval: (q) =>
      q.state.data?.files.some((f) => !f.failed && (isVideo(f.type) ? !f.hls : !f.url || !f.dims)) ? 3000 : false,
  });
  const editable = new Map((editor.data?.files ?? []).map((f) => [f.name, f]));
  // The post's poster and hover preview, cut from one of its videos.
  const hasVideo = (files.data?.files ?? []).some((f) => isVideo(f.type));
  const videoImages = useQuery({
    queryKey: ["post-media-video", postID],
    queryFn: ({ signal }) => uploads.getVideoImages(postRef(postID), undefined, signal),
    enabled: hasVideo,
    refetchInterval: (q) => (q.state.data?.poster.pending || q.state.data?.hover_preview.pending ? 3000 : false),
  });
  const imagesOf = videoImages.data;
  const encodePending = (editor.data?.files ?? []).some((f) => isVideo(f.type) && !f.hls && !f.failed);
  const encode = useQuery({
    queryKey: ["post-media-encode", postID],
    queryFn: () => readVideoProgress(postID),
    enabled: hasVideo,
    refetchInterval: (q) => (encodePending || q.state.data?.progress ? 2000 : false),
  });
  const imagesStep = encode.data?.progress?.phase === "images" ? encode.data.progress : undefined;
  const posterFile = imagesOf?.poster.selection?.file;
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
    onError: (e) => {
      setError(uploadMessage(e));
      toastMediaError(e, "Couldn't update the media");
    },
  });
  // Drops move a file at once; the server's order replaces the guess, or a failure restores it.
  const reorder = useMutation({
    mutationFn: ({ name, index }: { name: string; index: number }) => commit(postID, [{ op: "move", name, index }]),
    onMutate: async ({ name, index }) => {
      await client.cancelQueries({ queryKey: ["post-files", postID] });
      const prev = client.getQueryData<Awaited<ReturnType<typeof postFiles>>>(["post-files", postID]);
      if (prev) {
        const files = prev.files.filter((f) => f.name !== name);
        files.splice(index, 0, prev.files.find((f) => f.name === name)!);
        client.setQueryData(["post-files", postID], { ...prev, files });
      }
      return { prev };
    },
    onError: (e, _v, ctx) => {
      if (ctx?.prev) client.setQueryData(["post-files", postID], ctx.prev);
      toastMediaError(e, "Couldn't reorder the media");
    },
    onSettled: refresh,
  });
  // A draft commits the uploaded head of the queue, so the post keeps the order files were added in.
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
  // A draft commits each file as it finishes, so it can be cropped or picked as
  // the teaser before publishing; a live post commits on "Add to post".
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
  // A file the processor refuses after upload (an encode or image job) is toasted when it happens.
  const known = useRef<Set<string> | null>(null);
  useEffect(() => {
    const files = editor.data?.files;
    if (!files) return;
    const failed = files.filter((f) => f.failed && f.name).map((f) => ({ f, key: `${f.name}:${f.failed}` }));
    if (!known.current) {
      known.current = new Set(failed.map((x) => x.key));
      return;
    }
    for (const { f, key } of failed) {
      if (known.current.has(key)) continue;
      known.current.add(key);
      const title = `${displayName(f.name!)} couldn't be processed`;
      if (isVideo(f.type)) toast.error(title, { description: failureMessage(f.failed!) });
      else toastMediaError({ code: f.failed_code ?? "internal_error", message: f.failed, details: f.failed_details, refusal: !!f.failed_code }, title);
    }
  }, [editor.data]);
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
    if (refused.length) toast.error(refused.length === 1 ? "A file was not added" : `${refused.length} files were not added`, { description: refused.join(" ") });
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
  const count = images.length + queue.items.length;
  useEffect(() => onCount?.(count), [count, onCount]);
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
      <div className="grid gap-1">
        <SortableList
          className="media-list"
          items={images}
          id={(f) => f.name}
          name={(f) => displayName(f.name)}
          onMove={(from, to) => reorder.mutate({ name: images[from].name, index: index(images[to].name) })}
        >
          {(f, handle) => {
          const e = editable.get(f.name);
          const video = isVideo(f.type);
          return (
            <>
              {handle}
              <MediaThumb video={video} url={video && posterFile === f.name ? imagesOf?.poster.outputs[0]?.url : e?.url} />
              <span className="media-name" title={f.name}>
                {displayName(f.name)}
              </span>
              {f.edit && <Badge variant="outline">Edited</Badge>}
              {video && e?.hls && posterFile === f.name && (
                <Badge variant="secondary">
                  <HugeiconsIcon icon={Image01Icon} data-icon="inline-start" />
                  Cover
                </Badge>
              )}
              {video && e && !e.hls && !e.failed && <div className="media-encode"><EncodeProgress progress={e.progress} /></div>}
              {e?.failed && video && (
                <Badge variant="destructive" title={failureMessage(e.failed)}>
                  Can't play
                </Badge>
              )}
              {e?.failed && !video && (
                <Badge
                  variant="destructive"
                  title={mediaMessage({ code: e.failed_code, message: e.failed, details: e.failed_details, refusal: true })}
                >
                  Not processed
                </Badge>
              )}
              {teaser?.original === f.original && (
                <Badge variant="secondary">
                  <HugeiconsIcon icon={BlurIcon} data-icon="inline-start" />
                  Teaser
                </Badge>
              )}
              <span className="inline-actions">
                {video && e?.hls && (
                  <>
                    <Button size="sm" variant="ghost" onClick={() => setPicking({ file: f.name, what: "poster" })}>
                      Set cover
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setPicking({ file: f.name, what: "preview" })}>
                      Hover preview
                    </Button>
                  </>
                )}
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
            </>
          );
          }}
        </SortableList>
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
            {item.status === "uploading" && item.progress && (
              <progress max={Math.max(1, item.progress.total)} value={item.progress.loaded} />
            )}
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
      {imagesStep && (
        <div className="media-encode media-images">
          <EncodeProgress progress={imagesStep} />
        </div>
      )}
      {picking && (
        <>
          <VideoPosterPicker
            open={picking.what === "poster"}
            onOpenChange={(open) => !open && setPicking(undefined)}
            item={postRef(postID)}
            file={picking.file}
            onChange={(v) => {
              client.setQueryData(["post-media-video", postID], v);
              void client.invalidateQueries({ queryKey: ["posts"] });
            }}
          />
          <HoverPreviewPicker
            open={picking.what === "preview"}
            onOpenChange={(open) => !open && setPicking(undefined)}
            item={postRef(postID)}
            file={picking.file}
            onChange={(v) => client.setQueryData(["post-media-video", postID], v)}
          />
        </>
      )}
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
          minWidth={slotSpec?.min_width}
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
                if (m.pending) throw new UploadError("render_timeout", "the image is still rendering");
                if (m.error) throw slotFailure(m);
                saved(ref, slot, m);
                setNotice(`Channel ${slot} updated.`);
                setCropping(undefined);
              })
              .catch((err: unknown) => {
                setSlotError(messages.error(err));
                toastMediaError(err, "slot.save");
              })
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
    <span className="media-thumb" data-video={video || undefined}>
      {url ? (
        <img src={url} alt="" loading="lazy" />
      ) : (
        <HugeiconsIcon icon={video ? Video01Icon : Image01Icon} size={18} />
      )}
    </span>
  );
}

// processingLabel is a queue row's state while the media worker processes the
// file before "Add to post".
function processingLabel(item: QueueItem): string {
  const p = item.processing;
  if (p?.failed) return `can't process: ${failureMessage(p.failed)}`;
  if (item.processed) return "processed";
  if (p?.progress && p.progress.phase !== "queued") return `processing ${Math.round(p.progress.percent)}%`;
  return "processing…";
}
