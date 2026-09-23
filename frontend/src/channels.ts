import { useInfiniteQuery } from "@tanstack/react-query";
import { request } from "./api";
import { useAuth } from "./auth-context";
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
  channel?.offers?.find((offer) => offer.auto_renew);
