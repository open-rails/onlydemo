import { useQuery } from "@tanstack/react-query";
import { createUploadClient, UploadError } from "@openrails/contentkit-upload";
import type { FileInfo, ReadResult, RefBody } from "@openrails/contentkit-upload";
import { mediaMessage } from "./media-errors";
import { auth } from "./api";

// Browser uploads go straight to the bucket; the app only presigns, commits
// and reads (ContentKit's upload and read APIs behind AuthKit).
export const uploads = createUploadClient({
  endpoint: "/api/v1/media/upload",
  readEndpoint: "/api/v1/media",
  fetch: (input, init) => auth.authFetch(input, init),
});

export const postRef = (id: string): RefBody => ({ kind: "post", id });
export const channelRef = (id: string): RefBody => ({ kind: "channel", id });
export const userRef = (id: string): RefBody => ({ kind: "user", id });

// Mirrors the post kind's uploads (media.go); the server enforces them.
export const limits = {
  images: 40,
  videos: 10,
  imageBytes: 25 << 20,
  videoBytes: 20 * 1024 ** 3,
};
export const imageTypes = ["image/jpeg", "image/png", "image/webp", "image/gif", "image/avif"]; // keep in sync with media.go imageTypes
export const videoTypes = ["video/mp4", "video/webm", "video/quicktime", "video/x-matroska"];
export const isVideo = (type?: string) => !!type?.startsWith("video/");

// A post's upload paths: ContentKit orders images and videos each in turn.
export const IMAGES = "images/";
export const VIDEOS = "videos/";
export const uploadPath = (file: File) =>
  (isVideo(file.type) ? VIDEOS : IMAGES) + `${crypto.randomUUID().slice(0, 8)}-${file.name}`;
export const displayName = (path: string) => path.replace(/^(images|videos)\//, "").replace(/^[0-9a-f]{8}-/, "");

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

// Playlists come from the app's read API (bearer auth); byte ranges from
// media-gateway, signed in the playlist (URL mode) or under the item cookie
// the playlist response sets (cookie mode, hence withCredentials).
export function mediaXhr(xhr: XMLHttpRequest, url: string) {
  xhr.open("GET", url, true);
  if (new URL(url, location.href).origin === location.origin) {
    const token = auth.getAccessToken();
    if (token) xhr.setRequestHeader("Authorization", `Bearer ${token}`);
  } else {
    xhr.withCredentials = !/[?&]t=/.test(url);
  }
}

/** A post's media as this viewer may have it: every file, or its preview and locked files. */
export const readPost = (id: string) => uploads.read(postRef(id));

/** A post's uploads as its editors see them: edits, processing, failures. */
export function usePostUploads(id: string, enabled = true) {
  return useQuery({
    queryKey: ["post-uploads", id],
    queryFn: ({ signal }) => uploads.read(postRef(id), { editor: true, signal }),
    enabled: enabled && !!id,
    refetchInterval: (q) => (q.state.data && processing(q.state.data) ? 2500 : false),
  });
}

/** An editor read's uploads, images then videos. */
export const uploadsOf = (read?: ReadResult) => (read?.files ?? []).filter((f) => f.upload && !f.unattached);
export const processing = (read: ReadResult) =>
  uploadsOf(read).some((f) => !f.failed && (f.staged || (f.pending?.length ?? 0) > 0));

export const commit = (id: string, ops: Parameters<typeof uploads.commit>[1]) => uploads.commit(postRef(id), ops);

export function uploadMessage(error: unknown) {
  if (!(error instanceof UploadError)) return mediaMessage(error);
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
export function failureMessage(f: FileInfo) {
  if (isVideo(f.type))
    return /aspect/i.test(f.failed?.message ?? "")
      ? "Its shape is not supported: videos must be between 1:2.4 (tall) and 2.4:1 (wide)."
      : "It could not be processed. Try another file or format.";
  return mediaMessage({ code: f.failed?.code ?? "internal_error", message: f.failed?.message, details: f.failed?.details, refusal: !!f.failed?.code });
}
