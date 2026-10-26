import { useQuery } from "@tanstack/react-query";
import type { MyChannel } from "@openrails/contentkit-ui/client";
import { contentkit } from "./media";
import { useAuth } from "./session";

export type ChannelRole = "owner" | "editor";

/** ContentKit resolves live AuthKit permissions; channel and group IDs need not match. */
export function useMyChannels() {
  const { user } = useAuth();
  return useQuery({
    queryKey: ["my-channels", user?.id],
    enabled: !!user,
    queryFn: async () => {
      const channels: MyChannel[] = [];
      let cursor: string | undefined;
      do {
        const page = await contentkit.channels.mine({ limit: 100, cursor });
        channels.push(...page.channels);
        cursor = page.next_cursor;
      } while (cursor);
      return channels.filter((channel) => channel.owner || channel.can_post || channel.writer).map((group) => ({ group, role: (group.owner ? "owner" : "editor") as ChannelRole }));
    },
  });
}

export function useChannelRoles() {
  const channels = useMyChannels();
  return { ...channels, data: channels.data && new Map(channels.data.map(({ group, role }) => [group.id, role])) };
}

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
