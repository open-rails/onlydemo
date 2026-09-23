import { useEffect, useState } from "react";
import { createUploadClient, UploadError } from "@open-rails/contentkit-upload";
import type { CommitFile, Op, RefBody } from "@open-rails/contentkit-upload";
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
  teaser?: boolean;
  locked?: boolean;
  variant?: string;
  url?: string;
}
export interface MediaRead {
  access: "full" | "preview" | "none";
  total: number;
  expires: number;
  files: MediaFile[];
}

// The read API resolves access once and returns URLs only for what this
// viewer may see (cookie mode sets the folder cookie for full access).
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
      return "This channel is out of storage.";
    case "too_large":
      return "That file is too large.";
    case "type_not_allowed":
      return "Only JPEG, PNG, WebP and GIF images are supported.";
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
