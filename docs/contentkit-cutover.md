# ContentKit cutover (#117, OnlyDemo #125)

OnlyDemo is configuration and glue around ContentKit, AuthKit and OpenRails.
ContentKit `next/117` (pinned at `56a1183`) runs embedded (`contentkit.go`):
its own schema (`CONTENT_SCHEMA`, default `contentkit`), its jobs on the host
River fleet (`RiverJobs`, `WithRiverClient`), billing through
`adapters/openrails.Billing`, and its routes at `/contentkit/v1`. The app
keeps no tables.

| Config (`contentkit.yaml`) | Holds |
| --- | --- |
| `post` kind | route `p`, presentation `post`, one hidden version; owned by its channel (`owner_group_id`), never a member of it |
| `channel` group kind | route `c`, `owner: user`, `sellable`, `auth_scope`; cover (3:1 banner) and avatar slots |
| media kinds | `post` (version media: images, videos, `large`, `hls`, blurred `preview`), `channel`, `user` (avatars) |
| offers | USD, minimum 0.50 content, 1.00 group; PSPs from `merchant.yaml` |

## What replaced what

| Deleted | Replacement |
| --- | --- |
| `demo.channels` | ContentKit group `kind=channel`: slug, names, `owner_user_id`, `auth_scope` |
| `demo.posts`, the app schema and migrations | ContentKit content `kind=post`, `owner_group_id` = channel; rule per policy: `public`, `group` (membership), `group_ppv` (members' purchase), `ppv` |
| `GET /api/v1/channels`, `/channels/:id-or-slug` | `GET /contentkit/v1/groups?kind=channel[&slug=]`, `GET /contentkit/v1/groups/{id}` |
| `PUT /api/v1/channels/:id/membership` | `PUT` / `DELETE /contentkit/v1/groups/{id}/offer` (`amount: "0"` free; `renew_hours` paid) |
| `POST /api/v1/channels/:id/join`, `/leave` | `POST` / `DELETE /contentkit/v1/groups/{id}/join` |
| `POST /api/v1/channels/:id/subscribe`, `POST /api/v1/posts/:code/checkout` | OpenRails `POST /billing/v1/me/orders` for the offer's price (billing-ui `OrderCheckout`); prices from `GET /billing/v1/products?keys=` by the access keys a decision sells |
| `GET`/`POST`/`DELETE /api/v1/channels/:id/members` | AuthKit `/auth/v1/groups/{id}/members[/users/{id}]`, `/auth/v1/users?username=`, invitations |
| `GET /api/v1/me` | AuthKit `/auth/v1/me`; ContentKit `GET /contentkit/v1/me/library`; OpenRails `/billing/v1/me/*` |
| `GET /api/v1/me/channels` | AuthKit `GET /auth/v1/me/groups` (gap 13) |
| `GET /api/v1/posts[?channel_id]` | `GET /contentkit/v1/contents?kind=post&mode=lock[&owner_group=]` (cards) |
| `GET`/`POST`/`PATCH`/`DELETE /api/v1/posts[/:code]` | `/contentkit/v1/contents[/{id}]`, `.../text/en`, `.../publish`, `.../offer`; codes `GET /contentkit/v1/codes/{code}` |
| SPA `/p/{CODE}/{slug}` router, OG head | ContentKit codes route; the SPA resolves the code |
| `ALL /api/v1/contentkit/*` (Runtime) | `/contentkit/v1/*` (Engine routes) |
| River `demo_sync_membership`, `demo_delete_channel`, offer sync and archive, draft sweep; advisory locks | ContentKit offer sync (`contentkit_offers`); gaps 11, 15 |
| host access gate, media `Resolver`/`CanUpload`, `ItemReady` publishing | ContentKit's catalog visibility and version media |
| `seed` (app routes), `smoke.go` (fake Stripe) | `seed` and `smoke` against ContentKit's routes on a running server |
| `media_integration_test.go`, `listing_cost_test.go` | `channels_integration_test.go`, `billing_nmi_integration_test.go` |

Kept as glue: `POST /api/v1/channels` and `DELETE /api/v1/channels/:id`
(`channels.go`) create and retire the channel's AuthKit group around
ContentKit's group writes, forwarded with the caller's credential, until
ContentKit provisions the AuthKit group (`adapters/authkit`). Billing notices
publish through `Engine.Client()`; account purge calls `Engine.EraseSubjects`.

## AuthKit roles

Each channel's AuthKit group (persona `channel`, id = group id = `auth_scope`):

| Permission | Owner | Editor | ContentKit checks it for |
| --- | --- | --- | --- |
| `channel:content:write` | yes | yes | editing the channel's posts |
| `channel:offers:write` | yes | no | prices of its posts and its membership |
| `channel:settings:manage` | yes | no | the channel's group writes |
| `channel:media:write` | yes | no | the channel's cover and avatar |
| `channel:settings:read`, `channel:members:read` | yes | yes | AuthKit |

Site admins (root `admin`) hold `root:content:write`,
`root:content:view-restricted`, `root:groups:write`, `root:offers:write`, the
comment and moderation staff permissions and OpenRails customer staff ones.

## Frontend

On `@openrails/contentkit-ui` as `next/117` builds it (vendored tarball,
`frontend/vendor/`): its client (contents, groups, codes, media,
notifications) and media components. `src/content.ts` is the one data layer:

| Module | Reads and writes |
| --- | --- |
| `pages/explore.tsx`, `components/cards.tsx` | `GET /contentkit/v1/contents` cards; `GET /contentkit/v1/groups?kind=channel` |
| `pages/channel.tsx` | group by slug, `?owner_group=` feed, group offer, joins, `SlotEditor` on `cover/{name}`, `avatar/{name}` |
| `pages/post.tsx`, `post-view.tsx`, `post-media.tsx` | code → content, version decision, `MediaGallery` on the version |
| `pages/compose.tsx`, `post-editor.tsx` | draft content, version media folder, text, rule, offer, publish |
| `components/pay.tsx` | OpenRails order for the sold key's price; free join through ContentKit |
| `components/channel-team.tsx` | AuthKit members, directory, invitations |
| `pages/account.tsx` | ContentKit library, AuthKit roles, billing-ui |

Missing from contentkit-ui on `next/117` (PR #215, stage 2): discovery
(lists, cards, search), offers (put, withdraw, join, library) and version
clients, and content components (cards, paywall, editor); `content.ts`
calls those routes directly. Replace it with the package's once they land.

## Left to do

- [ ] Creators make channels and posts without the admin workaround (lane 120-user-groups: gaps 1-3); then drop `channels.go` for ContentKit's `POST`/`DELETE /v1/groups`.
- [ ] `?owner_group=` and owner chips (lane 122-owner-group): the channel feed and post cards' channel.
- [ ] Locked bodies (lane 122-owner-group): the e2e asserts locked → no `body`, buyer → `body`.
- [ ] Group payloads carry offers, viewer decision and avatars (gap 4); then drop `groupKey`, the storefront and entitlement reads in `content.ts`.
- [ ] contentkit-ui stage 2: discovery, offers and content components replace `content.ts`.
- [ ] Re-run `TestChannelsOnContentKit`, `TestNMIBillingEndToEnd` and `TestSmoke` (smoke on a real server, creators without admin) once those land.

## Gaps

Status as of `next/117` `56a1183`.

1. Creators can't create user-owned groups (`POST /v1/groups` is staff-only). Lane 120-user-groups.
2. Group writes and offers admit only staff and `owner_user_id`. Lane 120-user-groups: `channel:settings:manage`, `channel:offers:write`.
3. Nothing provisions or retires the `auth_scope` AuthKit group. Lane 120-user-groups (`adapters/authkit`).
4. Group cards and details carry no avatar, offer terms or viewer decision. Lane 120-user-groups.
5. `Engine.Client()` has no typed group, content or offer methods. Lane 120-user-groups.
6. The Engine exposes no media registry, so the media worker builds its own (`workerRegistry`).
7. The Engine wires no upload limiter: OnlyDemo's files/hour, bytes/day and per-channel quota are gone.
8. Catalog checks owner-group editors with `content:write`, which no AuthKit persona can hold; a signed-in read of channel-owned content answers 503. Lane 120-user-groups: `channel:content:write`.
9. `GET /v1/contents` filters no owner group; cards and content carry no owner chip. Lane 122-owner-group.
10. Catalog serves a locked post's `body` to anyone who sees it. Lane 122-owner-group.
11. Deleting a group cascades nothing: nothing withdraws its members' offers or retires the AuthKit scope. Lane 120-user-groups (cascades).
12. OpenRails must refuse a `group_ppv` purchase without the group key; editors can still buy.
13. ContentKit has no route listing the groups a viewer edits or belongs to (`/me/groups`). Lane 120-user-groups.
14. A membership going from paid to free keeps renewing existing subscriptions.
15. ContentKit has no draft TTL; OnlyDemo swept drafts after 24 h.
16. Cards carry the decision but not the rule: a post card a viewer opens can't say it is members-only.
