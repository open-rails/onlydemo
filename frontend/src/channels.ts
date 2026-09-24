import { useInfiniteQuery } from "@tanstack/react-query";
import { request } from "./api";
import { useAuth } from "./session";
import type { Channel, Page } from "./models";

export function useChannels() {
  const { user } = useAuth();
  const query = useInfiniteQuery({
    queryKey: ["channels", user?.id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      request<Page<Channel>>(
        "/api/v1/channels" +
          (pageParam ? "?cursor=" + encodeURIComponent(pageParam) : ""),
      ),
    getNextPageParam: (last) =>
      last.has_more ? last.next_cursor || undefined : undefined,
  });
  return {
    ...query,
    data: query.data
      ? { data: query.data.pages.flatMap((page) => page.data) }
      : undefined,
  };
}
export function hue(seed: string | number) {
  let h = 0;
  for (const c of String(seed)) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}
export const membershipOffer = (channel?: Channel) =>
  channel?.membership.status === "open" && !channel.membership.free
    ? channel.membership.offer || undefined
    : undefined;
// Whether a non-member can join now: a free or priced open membership.
export const canJoin = (channel?: Channel) =>
  !!channel &&
  !channel.membership.member &&
  channel.membership.status === "open" &&
  (channel.membership.free || !!channel.membership.offer);

export const myChannelsKey = (user?: string) => ["my-channels", user];
const lastKey = (user: string) => `onlydemo-post-channel:${user}`;

// The channel the composer preselects next time: last used or visited.
export function rememberPostChannel(user: string, channelID: string) {
  try {
    localStorage.setItem(lastKey(user), channelID);
  } catch {
    /* Falls back to the first channel. */
  }
}
export function recallPostChannel(user: string) {
  try {
    return localStorage.getItem(lastKey(user));
  } catch {
    return null;
  }
}
