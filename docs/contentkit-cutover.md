# ContentKit cutover (#117, OnlyDemo #125)

OnlyDemo is configuration and glue around ContentKit, AuthKit and OpenRails.
ContentKit `next/117` (pinned at `1d40ad8`) runs embedded (`contentkit.go`):
its own schema (`CONTENT_SCHEMA`, default `contentkit`), its jobs on the host
River fleet (`RiverJobs`, `WithRiverClient`), billing through
`adapters/openrails.Billing`, the channels' AuthKit groups through
`adapters/authkit.Scopes`, and its routes at `/contentkit/v1`. The app keeps
no tables and no content routes.

| Config (`contentkit.yaml`) | Holds |
| --- | --- |
| `post` kind | route `p`, presentation `post`, one hidden version; owned by its channel (`owner_group_id`), never a member of it |
| `channel` group kind | route `c`, `owner: user`, `sellable`, `auth_scope`; cover (3:1 banner) and avatar slots |
| media kinds | `post` (version media: images, videos, `large`, `hls`, blurred `preview`), `channel`, `user` (avatars) |
| offers | USD, minimum 0.50 content, 1.00 group; PSPs from `merchant.yaml` |

## What replaced what

| Deleted | Replacement |
| --- | --- |
| `demo.channels`, `POST`/`DELETE /api/v1/channels` | ContentKit group `kind=channel` (`POST`/`DELETE /contentkit/v1/groups`): slug, names, `owner_user_id`; ContentKit provisions and retires its AuthKit group (`auth_scope` = its id) |
| `demo.posts`, the app schema and migrations | ContentKit content `kind=post`, `owner_group_id` = channel; rule per policy: `public`, `group` (membership), `group_ppv` (members' purchase), `ppv` |
| `GET /api/v1/channels`, `/channels/:id-or-slug` | `GET /contentkit/v1/groups?kind=channel[&slug=]`, `GET /contentkit/v1/groups/{id}`: with `avatar` and `access` (key, offer terms, the viewer's membership) |
| `PUT /api/v1/channels/:id/membership` | `PUT` / `DELETE /contentkit/v1/groups/{id}/offer` (`amount: "0"` free; `renew_hours` paid) |
| `POST /api/v1/channels/:id/join`, `/leave` | `POST` / `DELETE /contentkit/v1/groups/{id}/join` |
| `POST /api/v1/channels/:id/subscribe`, `POST /api/v1/posts/:code/checkout` | OpenRails `POST /billing/v1/me/orders` for the offer's price (billing-ui `OrderCheckout`); the price id from `GET /billing/v1/products?keys=` by the key ContentKit sells (gap 10) |
| `GET`/`POST`/`DELETE /api/v1/channels/:id/members` | AuthKit `/auth/v1/groups/{id}/members[/users/{id}]`, `/auth/v1/users?username=`, invitations |
| `GET /api/v1/me` | AuthKit `/auth/v1/me`; ContentKit `GET /contentkit/v1/me/library`; OpenRails `/billing/v1/me/*` |
| `GET /api/v1/me/channels` | AuthKit `GET /auth/v1/me/groups` (gap 1) |
| `GET /api/v1/posts[?channel_id]` | `GET /contentkit/v1/contents?kind=post&mode=lock[&owner_group=]` (cards) |
| `GET`/`POST`/`PATCH`/`DELETE /api/v1/posts[/:code]` | `/contentkit/v1/contents[/{id}]`, `.../text/en`, `.../publish`, `.../offer`; codes `GET /contentkit/v1/codes/{code}` |
| SPA `/p/{CODE}/{slug}` router, OG head | ContentKit codes route; the SPA resolves the code |
| `ALL /api/v1/contentkit/*` (Runtime) | `/contentkit/v1/*` (Engine routes) |
| River `demo_sync_membership`, `demo_delete_channel`, offer sync and archive, draft sweep; advisory locks | ContentKit offer sync (`contentkit_offers`) and group delete cascades; gap 8 |
| host access gate, media `Resolver`/`CanUpload`, `ItemReady` publishing | ContentKit's catalog visibility and version media |
| `seed` (app routes), `smoke.go` (fake Stripe) | `seed` and `smoke` against ContentKit's routes on a running server |
| `media_integration_test.go`, `listing_cost_test.go` | `channels_integration_test.go`, `billing_nmi_integration_test.go` |

Billing notices publish through `Engine.Client()`; account purge calls
`Engine.EraseSubjects`.

## AuthKit roles

Each channel's AuthKit group (persona `channel`, id = group id = `auth_scope`,
provisioned by ContentKit with the creator as owner; the persona declares
`access.GroupPermissions("channel")`):

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
`frontend/vendor/`, built from `56a1183`): its client (contents, groups, codes, media,
notifications) and media components. `src/content.ts` is the one data layer:

| Module | Reads and writes |
| --- | --- |
| `pages/explore.tsx`, `components/cards.tsx` | `GET /contentkit/v1/contents` cards; `GET /contentkit/v1/groups?kind=channel` |
| `pages/channel.tsx` | group create, read, delete; `?owner_group=` feed; group offer and `access`; joins; `SlotEditor` on `cover/{name}`, `avatar/{name}`, selected by `PATCH` |
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

- [ ] `?owner_group=`, owner chips and locked bodies (lane 122-owner-group): the e2e asserts locked → no `body`, buyer → `body`.
- [ ] Decisions and group `access` carry the offer's price id (gap 10); then drop the storefront read in `content.ts` and `pay.tsx`.
- [ ] contentkit-ui stage 2 (#215): discovery, offers and content components replace `content.ts`.

## Gaps

Status as of `next/117` `1d40ad8`. Fixed by 120-user-groups (#217): creator
group writes, settings and offer editors, `adapters/authkit` scopes, group
`avatar` and `access`, the generated `Engine.Client()`, `channel:content:write`
for owned content (no more 503), group delete cascades, `/v1/me/groups`.

1. `/v1/me/groups` lists groups the viewer owns, manages (`settings:manage`) or belongs to, not those they only write content in; the composer reads AuthKit's `/auth/v1/me/groups`.
2. The Engine exposes no media registry, so the media worker builds its own (`workerRegistry`).
3. The Engine wires no upload limiter: OnlyDemo's files/hour, bytes/day and per-channel quota are gone.
4. `GET /v1/contents` filters no owner group; cards and content carry no owner chip. Lane 122-owner-group.
5. Catalog serves a locked post's `body` to anyone who sees it. Lane 122-owner-group.
6. OpenRails must refuse a `group_ppv` purchase without the group key; editors can still buy.
7. A membership going from paid to free keeps renewing existing subscriptions.
8. ContentKit has no draft TTL; OnlyDemo swept drafts after 24 h.
9. Cards carry the decision but not the rule: a post card a viewer opens can't say it is members-only.
10. Decisions (`sell`) and group `access.offer` name keys and terms but no price id, so a checkout reads OpenRails' storefront for it.
