import { createContentKitClient, type RefBody } from "@openrails/contentkit-ui/client";
import { auth } from "./api";

// ContentKit behind AuthKit at /contentkit: uploads go straight to the
// bucket, the browser presigns, commits and reads. Cookies reach
// media-gateway for playback under the item cookie (cookie delivery).
export const contentkit = createContentKitClient({
  baseUrl: "/contentkit",
  fetch: (input, init) => auth.authFetch(input, init),
  token: () => auth.getAccessToken(),
  credentials: "include",
});

// A post's media is its one version's item.
export const postRef = (id: string, version: string): RefBody => ({ kind: "post", id, version });
export const channelRef = (id: string): RefBody => ({ kind: "channel", id });
export const userRef = (id: string): RefBody => ({ kind: "user", id });

// A post's upload paths: ContentKit orders images and videos each in turn.
export const postPaths = ["images/{name}", "videos/{name}"];
