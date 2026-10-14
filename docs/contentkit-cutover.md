# ContentKit cutover (#117, OnlyDemo #125)

OnlyDemo is configuration and glue around ContentKit, AuthKit and OpenRails.
ContentKit runs embedded (`contentkit.go`):
its own schema (`CONTENT_SCHEMA`, default `contentkit`), its jobs on the host
River fleet (`RiverJobs`, `WithRiverClient`), billing through
`adapters/openrails.Billing`, the channels' AuthKit groups through
`adapters/authkit.Scopes`, and its routes at `/contentkit/v1`. The app keeps
no tables and no content routes.

| Config (`contentkit.yaml`) | Holds |
| --- | --- |
| `post` kind | route `p`, presentation `post`, one hidden version; owned by its channel (`owner_group_id`), never a member of it |
| `channel` group kind | route `c`, `owner: user`, `sellable`, `auth_scope`, upload limits; cover (3:1 banner) and avatar slots |
| media kinds | `post` (version media: images, videos, `large`, `hls`, blurred `preview`), `channel`, `user` (avatars) |
| offers | USD, minimum 0.50 content, 1.00 group; PSPs from `merchant.yaml` |

## What replaced what

| Deleted | Replacement |
| --- | --- |
| `demo.channels`, `POST`/`DELETE /api/v1/channels` | ContentKit group `kind=channel` (`POST`/`DELETE /contentkit/v1/groups`): slug, names, `owner_user_id`; ContentKit provisions and retires its AuthKit group (`auth_scope` = its id) |
| `demo.posts`, the app schema and migrations | ContentKit content `kind=post`, `owner_group_id` = channel; rule per policy: `public`, `group` (membership), `group_ppv` (members' purchase), `ppv`; a members-only post stays locked while its channel sells no membership |
| `GET /api/v1/channels`, `/channels/:id-or-slug` | `GET /contentkit/v1/groups?kind=channel[&slug=]`, `GET /contentkit/v1/groups/{id}`: with `avatar` and `access` (key, offer terms, the viewer's membership) |
| `PUT /api/v1/channels/:id/membership` | `PUT` / `DELETE /contentkit/v1/groups/{id}/offer` (`amount: "0"` free; `renew_hours` paid) |
| `POST /api/v1/channels/:id/join`, `/leave` | `POST` / `DELETE /contentkit/v1/groups/{id}/join` |
| `POST /api/v1/channels/:id/subscribe`, `POST /api/v1/posts/:code/checkout` | OpenRails `POST /billing/v1/me/orders` for the price ContentKit's sale names (`decision.offers[].price_id`, group `access.offer.price_id`), paid in billing-ui's `OrderCheckout` |
| `GET`/`POST`/`DELETE /api/v1/channels/:id/members` | AuthKit `/auth/v1/groups/{id}/members[/users/{id}]`, `/auth/v1/users?username=`, invitations |
| `GET /api/v1/me` | AuthKit `/auth/v1/me`; ContentKit `GET /contentkit/v1/me/library`; OpenRails `/billing/v1/me/*` |
| `GET /api/v1/me/channels` | AuthKit `GET /auth/v1/me/groups` (gap 1) |
| `GET /api/v1/posts[?channel_id]` | `GET /contentkit/v1/contents?kind=post&mode=lock[&owner_group=]` (cards) |
| `GET`/`POST`/`PATCH`/`DELETE /api/v1/posts[/:code]` | `/contentkit/v1/contents[/{id}]`, `.../text/en`, `.../publish`, `.../offer`; codes `GET /contentkit/v1/codes/{code}` |
| SPA post and channel addresses | `/en/p/{CODE}/{slug}` and `/en/c/{CODE}/{slug}`; the API owns paths, the shared canonical hook redirects unprefixed requests |
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
comment, moderation and avatar staff permissions and OpenRails customer staff ones.

## Frontend

The app keeps page composition around `@openrails/contentkit-ui`:

| Page | contentkit-ui |
| --- | --- |
| home (`pages/explore.tsx`) | `ContentGrid` of `PostCard`s with their owner chip, suggested channels (`useGroups`) |
| channels (`/c`) | `GroupDirectory` |
| channel (`pages/channel.tsx`) | `GroupBanner` with `MembershipBox`, `ContentGrid` (`owner_group`, no owner chip), `GroupImageEditor` (cover, avatar), group offers (`offers.sellGroup`), `groups.create`/`remove` |
| post (`pages/post.tsx`) | `useContentDetail`, `MediaGallery` on the version, `Paywall` |
| compose, edit | `ContentEditor` with the channel as owner group |
| account library | `offers.library` |

The app's own: `components/pay.tsx` orders the price a `Paywall` or
`MembershipBox` checkout names and shows billing-ui's `OrderCheckout`;
`roles.ts` reads the viewer's channel roles from AuthKit (gap 1);
`components/channel-team.tsx` is AuthKit's members, directory and invitations.

## Gaps

Status as of `v0.82.0`. Fixed since phase A: creator group and
content writes with `channel:*` permissions (#217), `adapters/authkit`
scopes, group `avatar` and `access`, the generated `Engine.Client()`,
`/v1/me/groups`, the `owner_group` filter and owner chips, locked bodies,
sales with price ids on decisions and group access, owner-group editors
opening their content, and OpenRails refusing a `group_ppv` purchase
without the group key (#218).

1. `/v1/me/groups` lists the groups the viewer owns, manages or belongs to, not those where they only write content; the app reads channel roles from AuthKit (`roles.ts`).
2. The Engine exposes no media registry, so the media worker builds its own (`workerRegistry`).
3. ContentKit has no draft TTL; OnlyDemo swept drafts after 24 h.
4. A channel's editors can still buy its posts.

Upload limits are the `channel` kind's `uploads` (60 files an hour, 100 GiB a
day per editor, 500 GiB per channel); a membership made free or withdrawn
stops paid renewals (`StopRenewals`, proven in `TestNMIBillingEndToEnd`).
