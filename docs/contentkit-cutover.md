# ContentKit cutover (#117, OnlyDemo #125)

OnlyDemo becomes configuration and glue around ContentKit, AuthKit and
OpenRails. Phase A (this branch) runs ContentKit's Engine and moves channels
into it. Phase B moves posts, feeds, the library and the frontend into it once
ContentKit's catalog and discovery land on `next/117`.

## Phase A: done

ContentKit `next/117` (pinned at `a638bbb`) runs embedded (`contentkit.go`): its own schema
(`CONTENT_SCHEMA`, default `contentkit`), its jobs on the host River fleet
(`RiverJobs`, `WithRiverClient`), billing through `adapters/openrails.Billing`,
and its routes at `/contentkit/v1`. `contentkit.yaml` is the content model.

| Config | Holds |
| --- | --- |
| `post` kind | route `p`, presentation `post`, one hidden version, group kind `channel` |
| `channel` group kind | route `c`, `owner: user`, `sellable`, `auth_scope`; media: cover (3:1 banner) and avatar slots |
| media kinds | `post` (images, videos, `large`, `hls`, blurred `preview`), `channel`, `user` (avatars) |
| offers | USD, minimum 0.50 content, 1.00 group; PSPs from `merchant.yaml` |

Deleted in phase A, with what replaces each:

| Deleted | Replacement |
| --- | --- |
| `demo.channels` (handle, name, membership state, sync columns) | ContentKit group `kind=channel`: slug, names, `owner_user_id`, `auth_scope` |
| posts `slug`, `offer_status`, `offer_revision`, `publishing` state | phase B content state; posts publish at once until then |
| `GET /api/v1/channels` | `GET /contentkit/v1/groups?kind=channel` |
| `GET /api/v1/channels/:id-or-slug` | `GET /contentkit/v1/groups/{id}`, `GET /contentkit/v1/groups?kind=channel&slug=` |
| `PUT /api/v1/channels/:id/membership` | `PUT` / `DELETE /contentkit/v1/groups/{id}/offer` (`amount: "0"` free; `renew_hours` paid) |
| `POST /api/v1/channels/:id/join`, `/leave` | `POST` / `DELETE /contentkit/v1/groups/{id}/join` |
| `POST /api/v1/channels/:id/subscribe` | OpenRails `POST /billing/v1/me/orders` with the offer's `price_id` (billing-ui `OrderCheckout`); the host's block on that route is gone |
| `GET`/`POST`/`DELETE /api/v1/channels/:id/members` | AuthKit `/auth/v1/groups/{id}/members` and invitations |
| `GET /api/v1/me` | AuthKit `/auth/v1/me`; ContentKit `GET /contentkit/v1/me/library`; OpenRails `/billing/v1/me/*` |
| `GET /api/v1/me/channels` | gap 13 |
| `POST /api/v1/posts/:code/checkout` | phase B: `PUT /contentkit/v1/contents/{id}/offer`, then an OpenRails order |
| `/api/v1/posts/:code` (content codes) | `/api/v1/posts/:id` until phase B; codes are ContentKit's (`/contentkit/v1/codes/{code}`) |
| `ALL /api/v1/contentkit/*` (Runtime) | `/contentkit/v1/*` (Engine routes) |
| SPA `/p/{CODE}/{slug}` canonical router, OG head | phase B: ContentKit codes route; the SPA resolves `/contentkit/v1/codes/{code}` |
| River `demo_sync_membership`, `demo_delete_channel`, post offer sync and archive, draft sweep; per-channel advisory locks | ContentKit offer sync jobs (`contentkit_offers`); gaps 11, 15 |
| `billing_catalog.go`, `post_offers.go`, `membership.go`, `post_purchase.go`, `page.go`, `channel_views.go`, `content_urls.go`, `media.go`, `media_commands.go`, `querycount.go` | the routes above |
| commands `seed`, `urls backfill`, `media reprocess`, `media sweep-orphans` | phase B seed; ContentKit codes; gap 6 |
| `smoke.go`, `scripts/smoke.sh`, `listing_cost_test.go`, `media_integration_test.go` | `channels_integration_test.go`, `billing_nmi_integration_test.go`; phase B e2e |

Kept as glue: `POST /api/v1/channels` and `DELETE /api/v1/channels/:id`
(`channels.go`) create and retire the channel's AuthKit group around
ContentKit's group writes, which they forward with the caller's credential.
Billing notices (`notifications.go`) publish through `Engine.Client()`;
account purge calls `Engine.EraseSubjects`.

## AuthKit roles

Each channel's AuthKit group (persona `channel`, id = group id = `auth_scope`):

| Permission | Owner | Editor | Checked by |
| --- | --- | --- | --- |
| `channel:settings:manage` | yes | no | ContentKit group writes and offers (lane 120-user-groups) |
| `channel:media:write` | yes | no | ContentKit group media (cover, avatar) |
| `channel:posts:{read,create,edit,delete}` | yes | yes | host posts; phase B: ContentKit's content-editor permission in the owner group's scope (name: catalog lane) |
| `channel:settings:read`, `channel:members:read` | yes | yes | AuthKit |

Site admins (root `admin`) hold `root:groups:write`, `root:offers:write`,
`root:comments:moderate`, `root:comments:ban`, `root:moderation:review`, the
host's `root:posts:*` and OpenRails customer staff permissions.

## Phase B checklist

Backend:
- [ ] `posts.go`, `migrations/postgres/001_posts.up.sql`, the `demo` schema → content kind `post`: `POST`/`PATCH`/`DELETE /contentkit/v1/contents`, `owner_group` = channel, rule per old policy (`public`, `group`, `group_ppv`, `ppv`).
- [ ] `GET /api/v1/posts[?channel_id]` → `GET /contentkit/v1/contents?kind=post[&groups=<channel>]`, `mode=lock`, cursor paging.
- [ ] `GET /api/v1/posts/:id` → `GET /contentkit/v1/contents/{id}`; deleted-but-bought is ContentKit's visibility; media erased iff never sold.
- [ ] PPV: `PUT /contentkit/v1/contents/{id}/offer`; checkout is an OpenRails order for its `price_id`.
- [ ] Purchased library → `GET /contentkit/v1/me/library`.
- [ ] `postMedia` (`Deps.Resolver`, `Deps.CanUpload`), `newGate`, `workerRegistry`'s post exception → deleted; post media is version media.
- [ ] `channels.go` → frontend calls `POST`/`DELETE /contentkit/v1/groups` once ContentKit provisions the AuthKit group (`adapters/authkit`).
- [ ] `ckAPI` → deleted (or `Engine.Client()` typed methods).
- [ ] `notifications.go` → ContentKit's own OpenRails consumer when the engine runs it.
- [ ] seed: creators, channels, offers (paid, free, none), posts of every policy through ContentKit routes.
- [ ] smoke and real-stack e2e: a purchase unlocks a post; a membership unlocks the channel.

Frontend (`frontend/src`, on contentkit-ui):

| Module | Calls today | Phase B |
| --- | --- | --- |
| `pages/explore.tsx`, `components/cards.tsx`, `channels.ts` | `/api/v1/posts`, `/api/v1/channels` | contents and groups listings, ContentKit cards |
| `pages/post.tsx`, `post-view.tsx`, `post-media.tsx`, `policy-badge.tsx` | `/api/v1/posts/:code`, `/api/v1/channels/:id` | content detail, version media, paywall decision (`Allowed`, `Sell`, `Requires`) |
| `pages/compose.tsx`, `post-editor.tsx` | `/api/v1/me/channels`, `/api/v1/posts` | content create/edit routes, `MediaFolderEditor` on the version |
| `pages/channel.tsx` | `/api/v1/channels/*`, membership, join/leave | group detail, `PUT .../offer`, `POST`/`DELETE .../join`, `CoverUpload`/`AvatarUpload` on the group item |
| `components/channel-team.tsx` | `/api/v1/channels/:id/members` | AuthKit group members and invitations only |
| `components/pay.tsx`, `purchase-key.ts`, `billing.tsx` | `.../checkout`, `.../subscribe`, `.../join` | billing-ui `OrderCheckout` for the offer's `price_id`; join through ContentKit |
| `pages/account.tsx`, `notifications.tsx` | `/api/v1/me`, `/api/v1/config` | AuthKit me, ContentKit library and inbox, billing-ui |
| `media.ts`, `contentkit.tsx`, `paths.ts` | `/api/v1/contentkit` | `/contentkit` client base; paths from ContentKit codes |
| fake like and bookmark (`post-view.tsx`) | none | ContentKit reactions and favorites (gap 9) |

## Gaps

Status as of `next/117` `a638bbb` (engine, groups, access, offers, version media, discovery).

1. Creators can't create user-owned groups (`POST /v1/groups` is staff-only). Lane 120-user-groups.
2. Group PATCH/DELETE/restore and offers admit only staff and `owner_user_id`, not `{kind}:settings:manage` holders. Lane 120-user-groups.
3. Nothing provisions or retires the `auth_scope` AuthKit group. Lane 120-user-groups (`adapters/authkit`).
4. Group cards and details carry no avatar, offer terms or viewer decision. Lane 120-user-groups.
5. `Engine.Client()` has no typed group and offer methods. Lane 120-user-groups.
6. The Engine exposes no media registry, so the media worker mirrors it (`workerRegistry`).
7. The Engine wires no upload limiter: OnlyDemo's files/hour, bytes/day and per-channel quota are gone.
8. Host kinds can't expose or delete media: phase A posts publish no preview and keep deleted media. Ends in phase B.
9. `post` is still ContentKit's editorial post kind (decision 7): reactions and comments on `post` go to editorial posts.
10. A membership going from paid to free archives the price, and subscriptions keep renewing. OnlyDemo granted free access and cancelled renewals.
11. Deleting a group has no retention purge: nothing purges member content, the AuthKit scope or media.
12. OpenRails must refuse a `group_ppv` purchase without the group key. The host's "editors can't buy" rule is gone.
13. ContentKit has no route listing the groups a viewer edits or belongs to (`/me/groups`).
14. ContentKit doesn't check that a group- or group_ppv-level rule's group has had an offer. OnlyDemo refused membership posts on channels without one.
15. ContentKit has no draft TTL; OnlyDemo swept drafts after 24 h.
16. `EraseSubjects` may not erase a person's avatar item (kind `user`). Unverified.
