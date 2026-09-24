// Public URLs are derived from current slugs, never stored.
export const channelsPath = "/c";
export const newChannelPath = "/c/new";
export const channelPath = (slug: string) => `/c/${encodeURIComponent(slug)}`;
export const postPath = (post: { channel_slug: string; slug: string }) =>
  `${channelPath(post.channel_slug)}/${encodeURIComponent(post.slug)}`;
