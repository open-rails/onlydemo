import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useUploadQueue } from "@open-rails/contentkit-upload/react";
import type { Op } from "@open-rails/contentkit-upload";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  ArrowDown01Icon,
  ArrowUp01Icon,
  BlurIcon,
  Delete02Icon,
  ImageAdd01Icon,
  SquareLock02Icon,
} from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import {
  commit,
  postFiles,
  postRef,
  readPost,
  uploadMessage,
  uploads,
  type MediaFile,
} from "../media";
import { FormError } from "./states";

const TEASER = "teaser";
const pending = (files?: MediaFile[]) =>
  !!files?.some((f) => !f.locked && !f.url);

// What this viewer may see: every image with full access, otherwise the
// blurred teaser and a count of what a purchase or membership unlocks.
export function PostGallery({ postID, viewer }: { postID: number; viewer?: string }) {
  const media = useQuery({
    queryKey: ["post-media", postID, viewer],
    queryFn: () => readPost(postID, "large,blurred"),
    refetchInterval: (q) => (pending(q.state.data?.files) ? 2000 : false),
  });
  const data = media.data;
  if (!data || data.total === 0) return null;
  const full = data.access === "full";
  const shown = data.files.filter((f) => f.url && (full ? !f.teaser : true));
  const locked = data.files.filter((f) => f.locked).length;
  return (
    <div className="post-gallery">
      {shown.map((f) => (
        <figure key={f.index} className={f.teaser && !full ? "teaser" : undefined}>
          <img src={f.url} alt={f.name || ""} loading="lazy" />
          {f.teaser && !full && locked > 0 && (
            <figcaption>
              <HugeiconsIcon icon={SquareLock02Icon} size={22} />
              {locked} {locked === 1 ? "image" : "images"} locked
            </figcaption>
          )}
        </figure>
      ))}
      {pending(data.files) && (
        <p className="muted text-sm">
          <Spinner className="inline" /> Processing images…
        </p>
      )}
    </div>
  );
}

// Creator tools: upload with the ContentKit SDK (hashing, resumable
// multipart, reorder before commit), then reorder, remove or pick the teaser.
export function PostMediaEditor({ postID }: { postID: number }) {
  const client = useQueryClient();
  const files = useQuery({
    queryKey: ["post-files", postID],
    queryFn: () => postFiles(postID),
  });
  const queue = useUploadQueue(uploads, { ref: postRef(postID) });
  const [error, setError] = useState("");
  const refresh = () =>
    client.invalidateQueries({
      predicate: (q) =>
        (q.queryKey[0] === "post-media" || q.queryKey[0] === "post-files") &&
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
        <h3>Images</h3>
        <label className="upload-button">
          <HugeiconsIcon icon={ImageAdd01Icon} size={18} />
          Add images
          <input
            type="file"
            accept="image/jpeg,image/png,image/webp,image/gif"
            multiple
            hidden
            onChange={(e) => {
              setError("");
              queue.add(e.target.files || [], {
                name: (f) => `${crypto.randomUUID().slice(0, 8)}-${f.name}`,
              });
              if (queue.blocked) queue.start();
              e.target.value = "";
            }}
          />
        </label>
      </header>
      <ol className="media-list">
        {images.map((f, i) => (
          <li key={f.name}>
            <span className="media-name">{f.name}</span>
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
              <Button
                size="sm"
                variant="ghost"
                disabled={teaser?.original === f.original || edit.isPending}
                onClick={() => setTeaser(f.original)}
              >
                Use as teaser
              </Button>
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
            <span className="media-name">{item.file.name}</span>
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
      {!teaser && images.length > 0 && (
        <p className="muted text-sm">
          Pick a teaser: readers without access see it blurred.
        </p>
      )}
    </section>
  );
}
