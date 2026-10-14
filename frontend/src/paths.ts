import { createContentURLs, parseCode } from "@openrails/contentkit-ui/urls";

// ContentKit owns post and channel codes, slugs, and language prefixes.
export const contentURLs = createContentURLs({ routes: { post: "p", channel: "c" }, languages: ["en"] });
export const postLink = (post: { code: string; slug: string }) => ({ content_kind: "post", code: post.code, slug: post.slug });
export const postPath = (post: { code: string; slug: string; path?: string }) => post.path || contentURLs.path(postLink(post));
export const postByIDPath = (id: string) => `/post/${encodeURIComponent(id)}`;
export const editPostPath = (id: string) => `/post/${encodeURIComponent(id)}/edit`;
export { parseCode };

export const channelsPath = "/c";
export const newChannelPath = "/c/new";
export function channelPath(channel: { path?: string }) {
  if (!channel.path) throw new Error("The channel's canonical path is missing.");
  return channel.path;
}
export const searchPath = (q: string) => `/search?q=${encodeURIComponent(q)}`;
export const newPostPath = (channel?: string) => "/post/new" + (channel ? `?channel=${encodeURIComponent(channel)}` : "");
