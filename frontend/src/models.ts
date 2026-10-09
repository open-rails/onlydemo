import type { PublicImage } from "@openrails/contentkit-upload";
export type AccessPolicy = "public" | "membership" | "members_ppv" | "ppv";
// An OpenRails offer: a product's current price for its entitlement.
export interface Offer {
  product_id: string;
  product_key: string;
  product_display_name: string;
  price_id: string;
  price_key: string;
  unit_amount: string;
  currency: string;
  kind: "permanent" | "finite" | "recurring";
  access_duration_hours: number | null;
  billing_interval_hours: number | null;
}
export interface Channel {
  id: string;
  slug: string;
  name: string;
  description?: string;
  post_count?: number;
  role?: "owner" | "editor";
  can_manage: boolean;
  can_edit: boolean;
  avatar: PublicImage | null;
  cover: PublicImage | null;
  membership: ChannelMembership;
  deleted_at?: string | null;
}
// A channel optionally sells one membership: "none" until created, then
// "open" (free or paid) or "closed" to new joins. Existing members keep access.
export interface ChannelMembership {
  status: "none" | "open" | "closed";
  free: boolean;
  sync: "none" | "pending" | "active" | "failed";
  offer: Offer | null;
  member: boolean;
  free_member: boolean;
}
export interface Post {
  id: string;
  channel_id: string;
  channel_slug: string;
  channel_name?: string;
  author_id: string;
  // The post's URL is /p/{code}/{slug}; the slug follows the title.
  code: string;
  slug: string;
  title: string;
  body?: string;
  can_read: boolean;
  can_edit?: boolean;
  purchased?: boolean;
  has_membership?: boolean;
  channel_avatar?: PublicImage;
  access_policy: AccessPolicy;
  // "publishing": media still processing; only the channel's editors see it.
  state: "draft" | "publishing" | "published";
  media_readiness?: MediaReadiness;
  offer_status: "none" | "pending" | "active" | "failed";
  offers: Offer[];
  created_at: string;
  updated_at: string;
}
// ContentKit's media readiness: failed once nothing is processing and some
// files could not be processed.
export interface MediaReadiness {
  state: "ready" | "processing" | "failed";
  processing?: string[];
  failed?: string[];
}
export interface Page<T> {
  data: T[];
  has_more?: boolean;
  next_cursor?: string | null;
  total?: number;
}
export interface AccountData {
  user: { id: string; username: string; email?: string; avatar: PublicImage | null };
  manageable_channels: Channel[];
  purchased_posts: Post[];
}
export interface AppConfig {
  /** The buyer's country from a trusted edge header, else "". */
  country: string;
  /** The user avatar preset; a user's is this at their id. */
  user_avatar: PublicImage;
}
export const visibilityLabels: Record<AccessPolicy, string> = {
  public: "Public",
  membership: "Members only",
  members_ppv: "Members only, for sale",
  ppv: "For sale",
};
