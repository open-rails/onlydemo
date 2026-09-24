import type { SlotManifest } from "@openrails/contentkit-upload";
import type { PspConfig } from "@openrails/billing-ui";
export type AccessPolicy = "public" | "membership" | "members_ppv" | "ppv";
export interface Offer {
  product_id: string;
  product_key: string;
  product_name: string;
  price_id: string;
  price_key: string;
  unit_amount: string;
  currency: string;
  kind: string;
  access_duration_hours?: number | null;
  auto_renew: boolean;
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
  avatar: SlotManifest | null;
  cover: SlotManifest | null;
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
  id: number;
  channel_id: string;
  channel_slug: string;
  channel_name?: string;
  author_id: string;
  slug: string;
  title: string;
  body?: string;
  can_read: boolean;
  can_edit?: boolean;
  purchased?: boolean;
  has_membership?: boolean;
  channel_avatar?: SlotManifest;
  access_policy: AccessPolicy;
  offer_status: "none" | "pending" | "active" | "failed";
  offers: Offer[];
  created_at: string;
  updated_at: string;
}
export interface Page<T> {
  data: T[];
  has_more?: boolean;
  next_cursor?: string | null;
  total?: number;
}
export interface AccountData {
  user: { id: string; username: string; email?: string; avatar: SlotManifest | null };
  manageable_channels: Channel[];
  purchased_posts: Post[];
}
export interface Checkout {
  id: string;
  status: string;
  url?: string | null;
  mode: string;
  amount?: string;
  currency?: string;
  metadata?: Record<string, string>;
  membership_quote?: { product_name: string; cycle_hours: number };
  operation?: { id: string; status: string };
  failure?: { reason: string; message: string; field?: string };
  subscription_id?: string;
  payment_method_id?: string;
  payment_id?: string;
  payment?: { rail: string };
  rail_data?: { rail?: string };
}
export interface PaymentMethod {
  id: string;
  rail: string;
  psp_id: string;
  card?: {
    brand?: string;
    last4?: string;
    exp_month?: number;
    exp_year?: number;
  };
  health?: { active: boolean };
}
export interface ProviderOption {
  selector: string;
  psp_id: string;
  rail: string;
  mode: string;
}
export interface PaymentOptionsDocument {
  plan: import("@openrails/billing-ui").CheckoutPlan;
  options: ProviderOption[];
  psps: PspConfig[];
  price_id: string;
  product_id: string;
}
export interface AppConfig {
  psps: PspConfig[];
  billing_available: boolean;
  /** The buyer's country from a trusted edge header, else "". */
  country: string;
}
export const policyLabels: Record<AccessPolicy, string> = {
  public: "Free to read",
  membership: "Included with membership",
  members_ppv: "Members-only purchase",
  ppv: "One-time purchase",
};
export const terminalCheckout = (status?: string) =>
  ["succeeded", "failed", "expired", "canceled"].includes(status || "");
