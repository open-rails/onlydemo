import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createUploadClient, UploadError } from "@openrails/contentkit-upload";
import type { CommitFile, Edit, EncodeProgress, Op, RefBody, SlotManifest } from "@openrails/contentkit-upload";
import { auth, request } from "./api";

// Browser uploads go straight to the bucket; the app only presigns and commits.
export const uploads = createUploadClient({
  endpoint: "/api/v1/media/upload",
  fetch: (input, init) => auth.authFetch(input, init),
});

export const postRef = (id: number | string): RefBody => ({
  kind: "post",
  id: String(id),
});

export interface MediaFile {
  index: number;
  name?: string;
  type?: string;
  w?: number;
  h?: number;
  duration?: number;
  teaser?: boolean;
  locked?: boolean;
  hls?: boolean;
  failed?: string; // editors only: why the video cannot be encoded
  progress?: EncodeProgress; // live encode progress while the video is pending
  edit?: Edit;
  dims?: { w: number; h: number };
  variant?: string;
  url?: string;
}
export interface MediaDownload {
  key: string;
  name: string;
  type?: string;
  size?: number;
  url: string;
}
export interface MediaRead {
  access: "full" | "preview" | "none";
  total: number;
  expires: number;
  files: MediaFile[];
  downloads?: MediaDownload[];
}

// Mirrors the server's post ceilings (media.go); the server enforces them.
export const limits = {
  files: 50,
  videos: 10,
  imageBytes: 25 << 20,
  videoBytes: 20 * 1024 ** 3,
};
export const imageTypes = ["image/jpeg", "image/png", "image/webp", "image/gif"];
export const videoTypes = ["video/mp4", "video/webm", "video/quicktime", "video/x-matroska"];
export const isVideo = (type?: string) => !!type?.startsWith("video/");

// Browsers leave .mkv/.mov types empty or nonstandard; name them for presign.
const byExtension: Record<string, string> = {
  mkv: "video/x-matroska",
  mov: "video/quicktime",
  mp4: "video/mp4",
  m4v: "video/mp4",
  webm: "video/webm",
};
export function withMediaType(file: File) {
  if (imageTypes.includes(file.type) || videoTypes.includes(file.type)) return file;
  const type = byExtension[file.name.split(".").pop()?.toLowerCase() ?? ""];
  return type ? new File([file], file.name, { type, lastModified: file.lastModified }) : file;
}

// Client-side checks of the post ceilings; the server enforces them again.
export function screenFiles(list: Iterable<File>, have: { files: number; videos: number }) {
  let { files: count, videos } = have;
  const accepted: File[] = [];
  const refused: string[] = [];
  for (const file of [...list].map(withMediaType)) {
    const video = isVideo(file.type);
    if (!imageTypes.includes(file.type) && !videoTypes.includes(file.type))
      refused.push(`${file.name}: unsupported type.`);
    else if (file.size > (video ? limits.videoBytes : limits.imageBytes))
      refused.push(`${file.name}: over the ${video ? "20 GiB video" : "25 MiB image"} limit.`);
    else if (count >= limits.files) refused.push(`${file.name}: a post holds at most ${limits.files} files.`);
    else if (video && videos >= limits.videos)
      refused.push(`${file.name}: a post holds at most ${limits.videos} videos.`);
    else {
      accepted.push(file);
      count++;
      if (video) videos++;
    }
  }
  return { accepted, refused };
}

// HLS routes of the read API; relative playlists resolve under the master.
export const hlsBase = (postID: number | string, name: string) =>
  `/api/v1/media/post/${postID}/hls/${encodeURIComponent(name)}/`;

// The read API resolves access once and returns URLs only for what this
// viewer may see (cookie mode sets the folder cookie for full access).
// The item's current encode step, including the poster/preview pass after publish.
export const readVideoProgress = (id: number | string) =>
  request<{ progress?: EncodeProgress }>(`/api/v1/media/post/${id}/video-images`);

export const readPost = (id: number | string, variants: string) =>
  request<MediaRead>(`/api/v1/media/post/${id}?variant=${variants}`);

export const postFiles = (id: number | string) =>
  request<{ files: CommitFile[] }>(`/api/v1/posts/${id}/media`);

export const commit = (id: number | string, ops: Op[]) =>
  uploads.commit(postRef(id), ops);

export function uploadMessage(error: unknown) {
  if (!(error instanceof UploadError)) return String(error);
  switch (error.code) {
    case "rate_limited":
      return `Upload limit reached. Try again in ${Math.ceil((error.retryAfter ?? 60) / 60)} min.`;
    case "quota_exceeded":
      return "This channel's storage is full. Remove files from its posts to make room.";
    case "too_large":
      return "That file is too large (images 25 MiB, videos 20 GiB).";
    case "too_many_files":
      return `A post holds at most ${limits.files} files, ${limits.videos} of them videos.`;
    case "type_not_allowed":
      return "Only JPEG, PNG, WebP and GIF images and MP4, WebM, MOV and MKV videos are supported.";
    case "forbidden":
      return "You can't upload here.";
  }
  return error.message;
}

// Slots (avatars, covers): listings carry a manifest from the app API; a
// slot's full manifest (edit, source dims) is read for its editors.
export const channelRef = (id: string): RefBody => ({ kind: "channel", id });
export const userRef = (id: string): RefBody => ({ kind: "user", id });
const slotKey = (ref: RefBody, slot: string) => ["slot", ref.kind, ref.id, slot];

export function useSlot(ref: RefBody, slot: string, initial?: SlotManifest | null, enabled = true) {
  const q = useQuery({
    queryKey: slotKey(ref, slot),
    queryFn: ({ signal }) => uploads.getSlot(ref, slot, signal),
    enabled: enabled && !!ref.id,
    staleTime: Infinity,
  });
  return q.data ?? initial ?? null;
}

// Stores a saved slot and refetches the listings that embed it.
export function useSlotSaved() {
  const client = useQueryClient();
  return (ref: RefBody, slot: string, m: SlotManifest) => {
    client.setQueryData(slotKey(ref, slot), m);
    void client.invalidateQueries({ predicate: (q) => q.queryKey[0] !== "slot" });
  };
}
