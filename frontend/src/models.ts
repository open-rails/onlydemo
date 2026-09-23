// Application responses; commercial offer fields are shared with the backend
// contract. A readable post is decided by the server, never by a UI role.
export type AccessPolicy = 'public' | 'membership' | 'members_ppv' | 'ppv'
export interface OfferPrice { id: string; amount_cents: number; currency: string; interval?: string; active: boolean }
export interface Offer { id: string; name: string; prices: OfferPrice[]; active: boolean; mode: 'one_off' | 'subscription' }
export interface Channel {
  id: string; slug: string; name: string; description?: string; post_count?: number
  role?: 'owner' | 'editor'; can_manage?: boolean; can_edit?: boolean
  subscription_active?: boolean; subscription_id?: string; offers?: Offer[]; deleted_at?: string | null
}
export interface Post {
  id: number; channel_id: string; channel_name?: string; channel_slug?: string; author_id: string
  slug: string; title: string; body?: string; excerpt?: string; visibility: 'public' | 'private'
  can_read: boolean; can_edit?: boolean; purchased?: boolean; subscription_active?: boolean
  access_policy?: AccessPolicy; offers?: Offer[]; price_cents?: number | null; currency?: string
  created_at: string; updated_at: string
}
export interface Checkout { id: string; status: 'created' | 'requires_action' | 'succeeded' | 'failed' | 'expired' | 'canceled'; payment_status: 'unpaid' | 'paid' | 'no_payment_required'; url?: string | null; mode: string; metadata?: Record<string, string> }
export function policyOf(post: Post): AccessPolicy { return post.access_policy || (post.visibility === 'public' ? 'public' : post.price_cents != null ? 'ppv' : 'membership') }
export const policyLabels: Record<AccessPolicy, string> = { public: 'Free to read', membership: 'Included with membership', members_ppv: 'Members-only purchase', ppv: 'One-time purchase' }
