import { useQuery } from "@tanstack/react-query";
import type { GroupDetail } from "@openrails/contentkit-ui/client";
import { request } from "./api";
import { contentkit } from "./media";
import { useAuth } from "./session";

export type ChannelRole = "owner" | "editor";

// The viewer's roles in channels: their AuthKit groups (ContentKit's
// /me/groups lists only the groups one manages or belongs to).
export function useChannelRoles() {
  const { user } = useAuth();
  return useQuery({
    queryKey: ["channel-roles", user?.id],
    enabled: !!user,
    queryFn: async () => {
      const out = new Map<string, ChannelRole>();
      let cursor = "";
      do {
        const page = await request<{ data: { group: { id: string; persona: string }; role: string }[]; next_cursor?: string }>(
          `/auth/v1/me/groups?limit=100${cursor ? "&cursor=" + encodeURIComponent(cursor) : ""}`,
        );
        for (const m of page.data ?? []) {
          if (m.group.persona !== "channel") continue;
          if (m.role === "channel:owner") out.set(m.group.id, "owner");
          else if (m.role === "channel:editor") out.set(m.group.id, "editor");
        }
        cursor = page.next_cursor ?? "";
      } while (cursor);
      return out;
    },
  });
}

/** The channels the viewer writes in, with their roles. */
export function useMyChannels() {
  const roles = useChannelRoles();
  return useQuery({
    queryKey: ["my-channels", [...(roles.data?.entries() ?? [])]],
    enabled: !!roles.data,
    queryFn: async () => {
      const ids = [...roles.data!.keys()];
      const groups = await Promise.all(ids.map((id) => contentkit.groups.get(id).catch(() => null)));
      return groups.filter((g): g is GroupDetail => !!g).map((group) => ({ group, role: roles.data!.get(group.id)! }));
    },
  });
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
