import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useUploadQueue } from "@open-rails/contentkit-upload/react";
import type { Op } from "@open-rails/contentkit-upload";
import { HugeiconsIcon } from "@hugeicons/react";
import {
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
  withMediaType,
  type MediaDownload,
  type MediaFile,
} from "../media";
import { CropDialog } from "./crop-dialog";
import { FormError } from "./states";
import { VideoPlayer } from "./video-player";

const TEASER = "teaser";
const ready = (f: MediaFile) => (isVideo(f.type) ? !!f.hls : !!f.url);
const pending = (files?: MediaFile[]) =>
  !!files?.some((f) => !f.locked && !ready(f));
const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

function lockedLabel(files: MediaFile[]) {
  const videos = files.filter((f) => isVideo(f.type)).length;
  const label = plural(files.length, "item");
  return videos ? `${label} locked (${plural(videos, "video")})` : `${label} locked`;
}

// Per-quality downloads of one video: keys are "{file}-{height}p".
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
export function PostMediaEditor({ postID }: { postID: number }) {
  const client = useQueryClient();
  const files = useQuery({
    queryKey: ["post-files", postID],
    queryFn: () => postFiles(postID),
  });
  const queue = useUploadQueue(uploads, { ref: postRef(postID) });
  const [error, setError] = useState("");
  const [cropping, setCropping] = useState<string>();
  // The unedited "editor" variant, source dims and current edit per image.
  const editor = useQuery({
    queryKey: ["post-media-editor", postID],
    queryFn: () => readPost(postID, "editor"),
    refetchInterval: (q) =>
      q.state.data?.files.some((f) => !isVideo(f.type) && (!f.url || !f.dims)) ? 3000 : false,
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
        refused.push(`${file.name}: over the ${video ? "2 GiB video" : "25 MiB image"} limit.`);
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
                  onClick={() => setCropping(f.name)}
                >
                  <HugeiconsIcon icon={CropIcon} />
                </Button>
              )}
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
      {cropping && (
        <CropDialog
          title="Crop and rotate"
          src={editable.get(cropping)?.url}
          dims={editable.get(cropping)?.dims}
          initial={editable.get(cropping)?.edit}
          onSave={(e) => uploads.edit(postRef(postID), cropping, e).then(refresh)}
          onClose={() => setCropping(undefined)}
        />
      )}
      {queue.blocked && <FormError>{uploadMessage(queue.blocked)}</FormError>}
      <FormError>{error}</FormError>
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
