import { useEffect, useState } from "react";
import { createUploadClient, UploadError } from "@open-rails/contentkit-upload";
import type { CommitFile, Edit, Op, RefBody } from "@open-rails/contentkit-upload";
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

// HLS routes of the read API; relative playlists resolve under the master.
export const hlsBase = (postID: number | string, name: string) =>
  `/api/v1/media/post/${postID}/hls/${encodeURIComponent(name)}/`;

// The read API resolves access once and returns URLs only for what this
// viewer may see (cookie mode sets the folder cookie for full access).
export const readPost = (id: number | string, variants: string) =>
  request<MediaRead>(`/api/v1/media/post/${id}?variant=${variants}`);

// A channel's slot sources, for its managers' cropper.
export const readChannel = (id: string) =>
  request<MediaRead>(`/api/v1/media/channel/${id}?variant=editor`);

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

// A public slot (avatar, banner) is re-encoded by a background job after
// upload; bump the URL for a few seconds so the new image shows up.
export function useSlotVersion() {
  const [version, setVersion] = useState(0);
  const [polls, setPolls] = useState(0);
  useEffect(() => {
    if (polls <= 0) return;
    const t = setTimeout(() => {
      setVersion((v) => v + 1);
      setPolls((p) => p - 1);
    }, 1500);
    return () => clearTimeout(t);
  }, [polls]);
  const src = (url?: string) => (url && version ? `${url}?v=${version}` : url);
  return { src, refresh: () => setPolls(6) };
}
