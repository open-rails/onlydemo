import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import type { PublicPreset } from "@openrails/contentkit-ui/client";
import { APIError, request } from "./api";
import { useAuth } from "./session";
import type { AccessPolicy, Channel, Offer, Page, Post } from "./models";

// Channels, posts and their offers come from ContentKit's routes; prices from
// OpenRails' storefront; editorial roles from AuthKit.
export const ck = (path: string) => `/contentkit/v1${path}`;
const tenant = "onlydemo";

// Access keys a paywall sells. Group payloads carry their offer and the
// viewer's decision once ContentKit lane 120-user-groups lands; until then
// the channel's key is built here.
export const groupKey = (id: string) => `${tenant}:group:${id}`;
export const contentKey = (id: string) => `${tenant}:content:${id}`;

interface Rendition {
  url: string;
  w: number;
  h: number;
}
interface GroupCard {
  id: string;
  kind: string;
  slug: string;
  name: string;
  path?: string;
  count: number;
  cover?: { image?: string; renditions?: Rendition[] };
  avatar?: Rendition[];
}
interface GroupDetail extends GroupCard {
  owner_user_id?: string;
  auth_scope?: string;
  text?: { language: string; description?: string }[];
}
interface Decision {
  allowed: boolean;
  sell?: string[];
  requires?: string;
}
interface OwnerChip {
  id: string;
  slug: string;
  name: string;
  avatar?: Rendition[];
}
interface Card {
  id: string;
  code: string;
  path: string;
  title: string;
  version: { id: string };
  access: Decision;
  owner?: OwnerChip;
  published_at?: string;
}
interface Content {
  id: string;
  code: string;
  path: string;
  slug?: string;
  title: string;
  body?: string;
  state: Post["state"];
  access: { level: string; group_id?: string };
  owner_group_id?: string;
  owner?: OwnerChip;
  editor: boolean;
  versions: { id: string; decision: Decision }[];
  published_at?: string;
  created_at: string;
}
interface Product {
  id: string;
  key: string;
  display_name: string;
  archived?: boolean;
  prices?: {
    id: string;
    key: string;
    unit_amount: string | number;
    currency: string;
    archived?: boolean;
    access_duration_hours?: number | null;
    billing_interval_hours?: number | null;
  }[];
}

const preset = (name: string, renditions?: Rendition[]): PublicPreset | null =>
  renditions?.length ? ({ preset: name, renditions } as PublicPreset) : null;

export const policyOf = (rule: Content["access"]): AccessPolicy =>
  rule.level === "group" ? "membership" : rule.level === "group_ppv" ? "members_ppv" : rule.level === "ppv" ? "ppv" : "public";

// The access rule of a policy over its channel.
export const ruleOf = (policy: AccessPolicy, channel: string) =>
  policy === "membership"
    ? { level: "group", group_id: channel }
    : policy === "members_ppv"
      ? { level: "group_ppv", group_id: channel }
      : { level: policy };

// A card carries its decision, not its rule: what it sells says which.
const cardPolicy = (card: Card, channel: string): AccessPolicy => {
  const sell = card.access.sell ?? [];
  if (card.access.requires) return "members_ppv";
  if (sell.includes(contentKey(card.id))) return "ppv";
  if (channel && sell.includes(groupKey(channel))) return "membership";
  return "public";
};

/** Prices on sale per key, from OpenRails' public storefront. */
export async function offers(keys: string[]): Promise<Map<string, Offer[]>> {
  const out = new Map<string, Offer[]>();
  const wanted = [...new Set(keys)];
  if (!wanted.length) return out;
  const query = new URLSearchParams({ merchant: tenant });
  for (const key of wanted) query.append("keys", key);
  const page = await request<{ data: Product[] }>(`/billing/v1/products?${query}`, {}, false);
  for (const product of page.data ?? []) {
    if (product.archived) continue;
    const list: Offer[] = [];
    for (const price of product.prices ?? []) {
      if (price.archived) continue;
      list.push({
        product_id: product.id,
        product_key: product.key,
        product_display_name: product.display_name,
        price_id: price.id,
        price_key: price.key,
        unit_amount: String(price.unit_amount),
        currency: price.currency,
        kind: price.billing_interval_hours ? "recurring" : price.access_duration_hours ? "finite" : "permanent",
        access_duration_hours: price.access_duration_hours ?? null,
        billing_interval_hours: price.billing_interval_hours ?? null,
      });
    }
    out.set(product.key, list);
  }
  return out;
}

/** The channel keys the viewer holds (memberships, free or bought). */
async function heldGroups(signedIn: boolean): Promise<Set<string>> {
  const out = new Set<string>();
  if (!signedIn) return out;
  let cursor = "";
  do {
    const query = new URLSearchParams({ prefix: `${tenant}:group:`, limit: "100" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<{ data: { entitlement: string }[]; next_cursor?: string }>(`/billing/v1/me/entitlements?${query}`);
    for (const item of page.data ?? []) out.add(item.entitlement);
    cursor = page.next_cursor ?? "";
  } while (cursor);
  return out;
}

/** The viewer's roles in channels, from AuthKit. */
export async function channelRoles(signedIn: boolean): Promise<Map<string, "owner" | "editor">> {
  const out = new Map<string, "owner" | "editor">();
  if (!signedIn) return out;
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
}

/** Channels with their membership offer, the viewer's membership and role. */
async function channels(groups: GroupDetail[], signedIn: boolean): Promise<Channel[]> {
  const [prices, held, roles] = await Promise.all([offers(groups.map((g) => groupKey(g.id))), heldGroups(signedIn), channelRoles(signedIn)]);
  return groups.map((g) => {
    const sold = prices.get(groupKey(g.id));
    const role = roles.get(g.id);
    const offer = sold?.find((o) => o.kind === "recurring") ?? null;
    const free = !!sold && sold.length === 0;
    const member = held.has(groupKey(g.id));
    return {
      id: g.id,
      slug: g.slug,
      name: g.name,
      description: g.text?.find((t) => t.description)?.description,
      post_count: g.count,
      role,
      can_manage: role === "owner",
      can_edit: !!role,
      avatar: preset("avatar", g.avatar),
      cover: preset("cover", g.cover?.renditions),
      membership: { status: sold ? "open" : "none", free, offer, member, free_member: member && free },
    };
  });
}

export function useChannels() {
  const { user } = useAuth();
  const query = useInfiniteQuery({
    queryKey: ["channels", user?.id],
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const page = await request<{ groups: GroupCard[]; next_cursor?: string }>(
        ck(`/groups?kind=channel&sort=newest&limit=25${pageParam ? "&cursor=" + encodeURIComponent(pageParam) : ""}`),
        {},
        false,
      );
      return { data: await channels(page.groups, !!user), next_cursor: page.next_cursor };
    },
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  return { ...query, data: query.data ? { data: query.data.pages.flatMap((p) => p.data) } : undefined };
}

/** One channel by handle, or by id. */
export async function loadChannel(handle: string, signedIn: boolean): Promise<Channel> {
  let id = handle;
  if (!/^[0-9a-f]{8}-/.test(handle)) {
    const page = await request<{ groups: GroupCard[] }>(ck(`/groups?kind=channel&slug=${encodeURIComponent(handle)}`), {}, false);
    if (!page.groups.length) throw notFound();
    id = page.groups[0].id;
  }
  const detail = await request<GroupDetail>(ck(`/groups/${id}`), {}, false);
  return (await channels([detail], signedIn))[0];
}

export function useChannel(handle: string) {
  const { user } = useAuth();
  return useQuery({ queryKey: ["channel", handle, user?.id], queryFn: () => loadChannel(handle, !!user), enabled: !!handle });
}

/** The channels the viewer edits, from their AuthKit roles. */
export function useMyChannels() {
  const { user } = useAuth();
  return useQuery({
    queryKey: ["my-channels", user?.id],
    enabled: !!user,
    queryFn: async () => {
      const roles = await channelRoles(true);
      const details = await Promise.all([...roles.keys()].map((id) => request<GroupDetail>(ck(`/groups/${id}`), {}, false).catch(() => null)));
      return { data: await channels(details.filter((d): d is GroupDetail => !!d), true) };
    },
  });
}

/** Posts from cards, with the prices their paywalls sell. */
async function postsOf(cards: Card[], channel?: Channel): Promise<Post[]> {
  const prices = await offers(cards.flatMap((c) => (c.access.allowed ? [] : (c.access.sell ?? []))));
  return cards.map((card) => {
    const owner = channel ? { id: channel.id, slug: channel.slug, name: channel.name } : card.owner;
    const sold = (card.access.sell ?? []).flatMap((key) => prices.get(key) ?? []);
    return {
      id: card.id,
      version: card.version.id,
      channel_id: owner?.id ?? "",
      channel_slug: owner?.slug ?? "",
      channel_name: owner?.name,
      channel_avatar: channel ? channel.avatar : preset("avatar", card.owner?.avatar),
      code: card.code,
      slug: card.path.split("/").pop() ?? "",
      path: card.path,
      title: card.title,
      can_read: card.access.allowed,
      access_policy: cardPolicy(card, owner?.id ?? ""),
      state: "published",
      offers: sold,
      created_at: card.published_at ?? "",
    };
  });
}

/** The feed: every channel's posts, or one channel's (owner_group). */
export function usePostFeed(channel?: Channel, enabled = true) {
  const { user } = useAuth();
  const query = useInfiniteQuery({
    enabled,
    queryKey: ["posts", channel?.id ?? "", user?.id],
    initialPageParam: "",
    queryFn: async ({ pageParam }): Promise<Page<Post>> => {
      const params = new URLSearchParams({ kind: "post", mode: "lock", sort: "newest", limit: "20" });
      if (channel) params.set("owner_group", channel.id);
      if (pageParam) params.set("cursor", pageParam);
      const page = await request<{ contents: Card[]; next_cursor?: string }>(ck(`/contents?${params}`));
      return { data: await postsOf(page.contents, channel), next_cursor: page.next_cursor };
    },
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  return { ...query, data: query.data?.pages.flatMap((p) => p.data) };
}

/** A post with its channel, decision and the prices that unlock it. */
export async function loadPost(id: string, signedIn: boolean): Promise<{ post: Post; channel: Channel | null }> {
  const content = await request<Content>(ck(`/contents/${id}`));
  const version = content.versions[0];
  const decision = version?.decision ?? { allowed: false };
  const channelID = content.owner_group_id ?? "";
  const [channel, prices] = await Promise.all([
    channelID ? loadChannel(channelID, signedIn).catch(() => null) : null,
    offers(decision.allowed ? [] : (decision.sell ?? [])),
  ]);
  const policy = policyOf(content.access);
  return {
    channel,
    post: {
      id: content.id,
      version: version?.id ?? "",
      channel_id: channelID,
      channel_slug: channel?.slug ?? "",
      channel_name: channel?.name,
      channel_avatar: channel?.avatar,
      code: content.code,
      slug: content.slug ?? "",
      path: content.path,
      title: content.title,
      body: content.body,
      can_read: decision.allowed,
      can_edit: content.editor,
      purchased: decision.allowed && (policy === "ppv" || policy === "members_ppv") && !content.editor,
      has_membership: channel?.membership.member,
      access_policy: policy,
      state: content.state,
      offers: (decision.sell ?? []).flatMap((key) => prices.get(key) ?? []),
      created_at: content.published_at ?? content.created_at,
    },
  };
}

/** A content code's content id. */
export async function resolveCode(code: string): Promise<string> {
  const link = await request<{ content_kind: string; content_id: string }>(ck(`/codes/${encodeURIComponent(code)}`), {}, false);
  if (link.content_kind !== "post") throw notFound();
  return link.content_id;
}

export interface LibraryItem {
  content_id: string;
  title: string;
  deleted: boolean;
}

export function useLibrary() {
  const { user } = useAuth();
  return useInfiniteQuery({
    queryKey: ["library", user?.id],
    enabled: !!user,
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      request<{ items: LibraryItem[]; next_cursor?: string }>(
        ck(`/me/library?limit=50${pageParam ? "&cursor=" + encodeURIComponent(pageParam) : ""}`),
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
}

// Writes: a post's text, rule and price; a channel's membership and joins.
export const posts = {
  /** A draft in channel: its version is the composer's media folder. */
  async draft(channel: string, policy: AccessPolicy): Promise<{ id: string; version: string }> {
    const created = await request<Content>(ck("/contents"), {
      method: "POST",
      body: JSON.stringify({ kind: "post", owner_group_id: channel, access: ruleOf(policy, channel) }),
    });
    return { id: created.id, version: created.versions[0].id };
  },
  /** Sets a post's text, rule and price; publish publishes a draft. */
  async save(id: string, channel: string, input: { title: string; body: string; policy: AccessPolicy; price?: string }, publish: boolean) {
    await request(ck(`/contents/${id}/text/en`), { method: "PUT", body: JSON.stringify({ title: input.title, body: input.body }) });
    await request(ck(`/contents/${id}`), { method: "PATCH", body: JSON.stringify({ access: ruleOf(input.policy, channel) }) });
    if (input.price) await request(ck(`/contents/${id}/offer`), { method: "PUT", body: JSON.stringify({ amount: input.price }) });
    else await request(ck(`/contents/${id}/offer`), { method: "DELETE" }).catch(ignoreNotFound);
    if (publish) await request(ck(`/contents/${id}/publish`), { method: "POST", body: "{}" });
  },
  remove: (id: string) => request(ck(`/contents/${id}`), { method: "DELETE" }),
};

export const memberships = {
  /** Free ("0"), a monthly price, or null to stop selling. */
  set: (channel: string, amount: string | null) =>
    amount === null
      ? request(ck(`/groups/${channel}/offer`), { method: "DELETE" }).catch(ignoreNotFound)
      : request(ck(`/groups/${channel}/offer`), {
          method: "PUT",
          body: JSON.stringify(amount === "0" ? { amount } : { amount, renew_hours: 720 }),
        }),
  join: (channel: string) => request(ck(`/groups/${channel}/join`), { method: "POST" }),
  leave: (channel: string) => request(ck(`/groups/${channel}/join`), { method: "DELETE" }),
};

function notFound() {
  return new APIError("Not found.", 404, "not_found");
}
function ignoreNotFound(error: unknown) {
  if (!(error instanceof APIError) || error.status !== 404) throw error;
}
