import { createContentURLs, parseCode } from "@openrails/contentkit-ui/urls";

// Posts are ContentKit content URLs, /en/p/{CODE}/{slug}: the server reads only
// the code and owns the slug. Channels keep their handle, /c/{handle}.
export const contentURLs = createContentURLs({ routes: { post: "p" }, languages: ["en"] });
export const postLink = (post: { code: string; slug: string }) => ({ content_kind: "post", code: post.code, slug: post.slug });
export const postPath = (post: { code: string; slug: string; path?: string }) => post.path || contentURLs.path(postLink(post));
export const postByIDPath = (id: string) => `/post/${encodeURIComponent(id)}`;
export const editPostPath = (id: string) => `/post/${encodeURIComponent(id)}/edit`;
export { parseCode };

export const channelsPath = "/c";
export const newChannelPath = "/c/new";
export const channelPath = (slug: string) => `/c/${encodeURIComponent(slug)}`;
export const searchPath = (q: string) => `/search?q=${encodeURIComponent(q)}`;
export const newPostPath = (channel?: string) => "/post/new" + (channel ? `?channel=${encodeURIComponent(channel)}` : "");
