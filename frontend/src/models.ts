import type { PublicPreset } from "@openrails/contentkit-ui/client";
export type AccessPolicy = "public" | "membership" | "members_ppv" | "ppv";
// An OpenRails price on sale for an access key (the product's key).
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
// A channel: a ContentKit group whose AuthKit group holds its editors.
export interface Channel {
  id: string;
  slug: string;
  name: string;
  description?: string;
  post_count?: number;
  role?: "owner" | "editor";
  can_manage: boolean;
  can_edit: boolean;
  avatar: PublicPreset | null;
  cover: PublicPreset | null;
  membership: ChannelMembership;
}
// A channel sells at most one membership, its ContentKit group offer:
// "none" without one, else "open", free or at a renewing price.
export interface ChannelMembership {
  status: "none" | "open";
  free: boolean;
  offer: Offer | null;
  member: boolean;
  free_member: boolean;
}
// A post: ContentKit content of the post kind owned by its channel.
export interface Post {
  id: string;
  // The one (hidden) version holding its media.
  version: string;
  channel_id: string;
  channel_slug: string;
  channel_name?: string;
  channel_avatar?: PublicPreset | null;
  // The post's URL is /p/{code}/{slug}; the slug follows the title.
  code: string;
  slug: string;
  path: string;
  title: string;
  body?: string;
  can_read: boolean;
  can_edit?: boolean;
  purchased?: boolean;
  has_membership?: boolean;
  access_policy: AccessPolicy;
  state: "draft" | "published" | "deleted";
  offers: Offer[];
  created_at: string;
}
export interface Page<T> {
  data: T[];
  next_cursor?: string | null;
}
export interface AppConfig {
  /** The buyer's country from a trusted edge header, else "". */
  country: string;
}
export const visibilityLabels: Record<AccessPolicy, string> = {
  public: "Public",
  membership: "Members only",
  members_ppv: "Members only, for sale",
  ppv: "For sale",
};
