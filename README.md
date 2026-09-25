# OnlyDemo

A React/Fiber creator app: creators run channels and publish posts on them.
AuthKit handles people and publishing roles; OpenRails handles product offers,
purchases, recurring memberships and payment history.

The application API is `/api/v1`, identity JSON API is `/auth/v1`, and customer
billing is `/billing/v1`. The React app lives at `/`; the searchable native route
directory is `/dev/routes`. AuthKit protocol anchors stay at
`/.well-known/jwks.json` and `/oidc` when configured. The issuer stays the origin.

## Run locally

Prerequisites: Docker, Go 1.26.6, Node 24 with pnpm 11, libvips (`libvips-dev`;
the media worker's image jobs are CGO) and ffmpeg with ffprobe (its video jobs). Copy
`.env.example` to `.env` and set `MEDIA_TOKEN_KEY`
(`echo "dev:$(openssl rand -base64 32)"`), then:

```sh
task dev:up   # PostgreSQL (with PGroonga) and MinIO with the media bucket; idempotent
task run
task seed     # optional display channels and posts, created through the API
```

`task run` migrates (idempotent), then serves Go with Air, the frontend with
Vite, the media access worker (`task media:access`) and the media worker
(`task media:worker`, `onlydemo media-worker`), reloading on change; open http://localhost:5173.
The MinIO console is http://localhost:59001 (`onlydemo` / `onlydemo-dev-secret`). `task run:embedded` builds the frontend into
the single Go binary and serves everything from http://localhost:3000.
`task seed` targets http://127.0.0.1:3000; pass `-- --url <base>` for another
server. It is idempotent.
For payment flows while browsing Vite, start the backend with
`PUBLIC_URL=http://localhost:5173 task run` and keep using that exact browser
origin. Sessions and pending checkout attempts are stored per origin; switching
from 5173 to 3000 during a payment loses that browser context.

The frontend follows the shared UI standard: shadcn `base-vega` on
`@base-ui/react` (zinc, hugeicons, Tailwind 4, `cn@0.4.0`). Primitives in
`frontend/src/components/ui` come from `pnpm dlx shadcn@4.21.0 add <name>`; never
hand-edit them except for `// Local:` deltas. `frontend/dist` is committed and
embedded, so rebuild it with every frontend change.

Billing is required and sandbox-only. Enabling a provider is configuration only:
list its key in `BILLING_PSPS` (e.g. `stripe,nmi`) and set its variables.
OpenRails's `embed.PSPFromEnv` reads `<KEY>_ACCOUNT_ID` plus the rail's secrets and
settings (`<KEY>_RAIL` when the key is not the rail name) and refuses a missing
required secret; startup refuses a PSP whose credentials are not sandbox. The
browser receives OpenRails's browser-safe PSP config and `@openrails/billing-ui`
picks each flow (Collect.js, Stripe Elements, hosted redirect); there is no
provider-specific code here. `BILLING_CHECKOUT_PSP` picks the one PSP for new
purchases and new cards (applied as OpenRails's checkout routing); the others keep
serving their existing cards and subscriptions. Buyers see one payment modal:
saved cards, an inline new card (always saved), one Pay button.

| Provider | Variables |
| --- | --- |
| Stripe | `STRIPE_ACCOUNT_ID`, `STRIPE_SECRET_KEY` (`sk_test_`/`rk_test_`), `STRIPE_WEBHOOK_SIGNING_SECRET`, `STRIPE_PUBLISHABLE_KEY` (`pk_test_`, for in-page card setup) |
| NMI | `NMI_ACCOUNT_ID` (gateway ID), `NMI_SECURITY_KEY` (test mode), `NMI_WEBHOOK_SIGNING_SECRET`, `NMI_TOKENIZATION_KEY`, optional `NMI_TOKENIZATION_URL`, `NMI_ENDPOINT_DEPLOYMENT` (`gateway` or `sandbox`) |

Declared PSP settings seed a new database; an existing PSP row keeps its stored
settings.

The local sandbox has OpenRails's destructive-action switch armed, so terminal dunning
cancels, revocations and provider-schedule cancels execute; an operator disables them
with `UPDATE billing.destructive_action_switch SET enabled=false, updated_by='<you>', reason='<why>', updated_at=now();`.

Webhooks go to `/billing/v1/webhooks/<rail>/<ACCOUNT_ID>`. For Stripe run
`task stripe:listen`, copy its `whsec_...` into `STRIPE_WEBHOOK_SIGNING_SECRET` and
keep it running (it finds `stripe` on PATH, then `.runtime/bin/stripe`; restricted
keys need Debugging Tools: Write). NMI posts only to a public HTTPS URL: register
`https://<public-host>/billing/v1/webhooks/nmi/<NMI_ACCOUNT_ID>` in the NMI portal
with the signing key, through a tunnel when local. Locally it is optional: NMI
results are synchronous and OpenRails reconciles from the gateway.

| Setting | Purpose |
| --- | --- |
| `DATABASE_URL` | One owning PostgreSQL connection/pool for initialization, content, AuthKit, OpenRails and River |
| `PORT`, `PUBLIC_URL` | API listener and browser return origin |
| `RETURN_ORIGINS` | Comma-separated extra origins checkout/portal return URLs may name besides `PUBLIC_URL` (e.g. the Vite dev origin); any other return URL is refused |
| `AUTH_ISSUER`, `AUTH_AUDIENCE` | Token issuer/audience; origin issuer matches root discovery |
| `AUTH_KEYS_PATH` | Dev signing and TOTP key directory (default `.runtime/auth`), so sessions and authenticator apps survive restarts |
| `AUTH_SCHEMA`, `APP_SCHEMA`, `BILLING_SCHEMA`, `RIVER_SCHEMA` | Optional independent schema names; shared `public` is supported |
| `BILLING_PSPS` | Enabled PSP keys, e.g. `stripe,nmi`; each needs its variables (above) |
| `BILLING_CHECKOUT_PSP` | PSP for new purchases and cards; required with several `BILLING_PSPS` |
| `TRUSTED_COUNTRY_HEADER` | Edge header with the buyer's country (e.g. `CF-IPCountry`) to preselect the billing country; unset = never trusted, the browser locale is used |
| `MEMBERSHIP_PERIOD` | Renewal period of membership prices set afterwards, in whole hours (default `720h`; e.g. `1h` to test rebilling) |
| `POST_DELETION_REFUND`, `POST_DELETION_REFUND_WINDOW` | Deleting a paid post: `refund` (default), `review` or `none` for one-time purchases made within the window before deletion (default `720h`). A post that becomes free refunds nothing |
| `CONTENT_SCHEMA` | ContentKit baseline schema (default `content`); holds the upload limiter's counters |
| `MEDIA_S3_*` | Media bucket: `ENDPOINT`, `PUBLIC_ENDPOINT` (browser presign host), `BUCKET`, `REGION`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY` |
| `MEDIA_URL`, `MEDIA_DELIVERY`, `MEDIA_COOKIE_DOMAIN` | media-access origin; `cookie` (default) or `url` delivery |
| `MEDIA_TOKEN_KEY`, `MEDIA_TOKEN_KEY_PREVIOUS` | `{kid}:{base64 32+ bytes}` signing keys shared with media-access |
| `MEDIA_UPLOAD_FILES_PER_HOUR`, `MEDIA_UPLOAD_BYTES_PER_DAY`, `MEDIA_CHANNEL_QUOTA_BYTES` | Upload limits (defaults 60, 100 GiB, 500 GiB) |

There is no separate billing database URL or billing encryption key. Credentials
are supplied as a host-owned snapshot. The app and billing library use fresh
pre-v1 schema baselines: use a new database when the baseline changes, preserving
old databases and receipts. Startup validates checksums and never rewrites them.
AuthKit owns its forward migrations; River owns the shared worker schema.

## Channels and reading

Every post belongs to a channel. AuthKit is the only authority for publishing:
channel owners can manage the channel and share owner/editor roles; editors can
publish and edit but cannot manage owners or delete the channel. Ordinary readers,
subscribers and purchasers are **not** added to the publishing group.

URLs derive from current slugs: `/c` lists channels, `/c/new` creates one,
`/c/<channel>` and `/c/<channel>/<post>` (API:
`GET /api/v1/channels/<channel>/posts/<post>`). Post slugs are unique within
their channel. Checkout returns carry the stable post/channel id and resolve
the current URL, so renames never break a link.

Posts have one of four content policies:

- **Public:** free to read.
- **Membership:** included while channel membership is active, including future posts.
- **Members-only purchase:** active membership is required to start a purchase.
- **One-time purchase:** anyone signed in can buy.

A purchased post stays readable permanently, even after membership ends or the
post's access policy changes. Deleted content is hidden. Payment history remains.

The application stores content and its access policy, not a duplicate product,
price or sales ledger. OpenRails products grant opaque resource keys such as
`post:<stable-id>` and `channel:<channel-id>:membership`. Catalog offers own native
currency amounts, availability and immutable price versions. USD offers appear
first; another displayed currency is charged only when explicitly selected.
A channel has no membership until its owner creates one (`PUT
/api/v1/channels/:id/membership` with `{"enabled": bool, "price": {...} | null}`;
`null` is free, paid is at least $1.00). Membership and members-only posts
require one. States:

- **Open, paid:** new members subscribe; repricing affects new members only.
- **Open, free:** `POST /join` grants the membership resource directly (no card);
  `POST /leave` revokes it. Making a paid membership free stops paid renewals at
  period end and grants those members the free membership.
- **Closed** (`enabled: false`): no new joins. Existing members keep access,
  including new membership posts, and keep renewing at their accepted price.
  Free members stay members when a price is set again.

Channel membership renews every `MEMBERSHIP_PERIOD` (default `720h`: **every 30 days**,
not a calendar month); a change applies to prices set afterwards. Repricing moves a stable price key and preserves already accepted terms.

## Media

Images and videos use ContentKit media: one private bucket, a folder per item,
originals never served. Browsers upload straight to the bucket with
`@openrails/contentkit-upload` (a ContentKit release asset); the app presigns
and commits (`/api/v1/media/upload/*`).

Post ids are UUIDv7s (`uuidv7()`), ContentKit's content id and the media
folder `{tenant}/post/{id}/`, so an id is never reused after a database reset.
Creating a post starts its folder (`Manifests.Create`) and fails over leftovers.
`onlydemo media sweep-orphans [--grace 24h] [--delete]` reports or removes post
and channel folders with no row.

- **Kinds.** `post`: images (`large`, `thumb`) and videos (MP4, WebM, MOV,
  MKV) mixed in one order, plus an optional image `teaser` derived as a
  blurred WebP (the app refuses a video teaser). ContentKit holds the caps
  (`Kind.MaxFiles`, `TypeLimits`): 50 files per post (teaser included), 10 of
  them videos; images up to 25 MiB, videos up to 20 GiB (multipart, 4K sources); 409 `too_many_files`. `channel`: public `avatar` (1:1, 128/512 px) and
  `cover` (3:1, 900/3000 px, crops from 600 px) slots; `user`: `avatar`.
- **Formats.** Images: JPEG, PNG, WebP, GIF, AVIF (`imageTypes` in media.go
  and frontend/src/media.ts). Animated GIF/WebP stay animated everywhere
  (ContentKit's default `Animation` allow; no slot rejects). Animated AVIF is
  refused until libheif 1.19. Refusals are typed and toasted; errors surface
  as sonner toasts through `UploadUiProvider onError`.
- **Slots.** The SDK UI crops before saving (`AvatarUpload` on the account
  page; `SlotEditor` + `SlotEditMenu` over the channel header): the original
  is kept, EXIF orientation applied, and ContentKit renders the small and
  large rendition (the large one at the crop's width when narrower; never
  upscaled). "Edit crop" re-renders the kept original
  (`edit-slot`); "Use as channel avatar/cover" on a post image copies it
  (`commit-slot-from-file` with `from`; the manager must be allowed to upload to
  both). The `SlotEncoded` hook stores each slot's `SlotListing` in
  `media_slots.listing`; listings build its URLs with
  `Reader.ListedSlot(ref, slot, listing)` (hash-named, immutable files; no
  bucket reads). SDK components pick the rendition for their rendered width × 2–3×
  density. After changing widths or variant specs, run `onlydemo media
  reprocess` (one `ProcessJob` per post, channel and user with media).
- **Edits.** Crop and rotate are ContentKit's non-destructive file edits
  (commit op `edit`): variants re-derive from the untouched original. The
  post image cropper (react-easy-crop with the SDK's `useCrop`) draws the `Unedited`,
  `EditorOnly` `editor` variant. Only editors (whoever may upload to the item:
  its channel's posters or managers, site admins; `Resolution.Editor`) are
  signed it and get `edit`/`dims`. Item order lives in the
  manifest; the post row keeps only its access policy.
- **Reads.** `GET /api/v1/media/post/{id}?variant=large,blurred` resolves once
  with the post rule (`media/tiered` over OpenRails: membership key,
  `post:<billing_key>` purchase key, members_ppv = purchase only; channel
  editors and site admins read everything). Full access gets every file;
  anyone else who can see the post gets the teaser only. Files are served by
  `media-access`, never the bucket.
- **Media worker.** `onlydemo media-worker` (`task media:worker`) is
  ContentKit's media worker built with this app's kinds, teaser spec choice
  and hooks (`media_worker.go`): it does every image rendition, slot, video
  encode and the placement of multipart uploads (`staging/u-…` hashed while
  processed, then moved to `originals/sha256-…`). The app only presigns,
  commits, publishes and reads, enqueueing into River schema `media_worker`.
  Env: the app's config plus `MEDIA_WORKER_THREADS` (ffmpeg threads),
  `MEDIA_WORKER_CONCURRENCY`, `MEDIA_WORKER_IMAGE_CONCURRENCY`,
  `MEDIA_WORKER_ENCODER` (auto/x264/nvenc), `MEDIA_WORKER_TMP` and
  `MEDIA_HOST_RIVER_SCHEMA` (default: `RIVER_SCHEMA`).
- **Process on upload.** Each file is committed unattached the moment it
  uploads, so it is processed while the post is still being composed; the
  queue row shows the processing progress, "Add to post" attaches the files
  in order without reprocessing, and removing one cancels its jobs and
  deletes it.
- **Video.** A commit enqueues the encode in River schema `media_worker`;
  the media worker writes a
  byte-range HLS ladder (ContentKit's default rungs 2160–480 by short side, so
  vertical and 21:9 sources keep their aspect; capped at 4096 px per side and
  3840×2160 area), a seek sprite and one MP4 download per rung. Sources outside
  1:2.4–2.4:1 fail; editors see why (`failed`). The player (hls.js) loads
  `/api/v1/media/post/{id}/hls/{file}/master.m3u8` from the read API, sized at
  the source's `w`/`h`; segments come from `media-access`. Downloads are saved
  as `{post-slug}-{file}-{rung}p.mp4`.
- **Composer.** The first file added to "New post" creates the post as a
  draft (`POST /api/v1/posts {channel_id, draft: true}`: no slug, never
  listed, visible and uploadable only by its author), so uploads have a folder
  and can be cropped and picked as the teaser before publishing. Files commit
  in the order added; Publish sends `draft_id` and turns the draft into the
  post. Cancel deletes the draft with its folder (`DELETE /api/v1/posts/{id}`);
  drafts abandoned for 24 h are swept hourly.
- **Covers.** Each post with a video has a cover at the video's native aspect
  (`Video.PosterWidths` 640/1280/1920: the ~620 px feed column at 2–3×). The
  worker picks an automatic frame; in the composer and the post's media editor,
  "Set cover" (`VideoPosterPicker`: an exact frame via
  `GET /api/v1/media/upload/frame`, optionally cropped at the video's aspect, or
  an uploaded image) changes it. Frame posters reach the app's image job in the
  same worker; the app needs ffmpeg for `/frame`. `SlotEncoded` stores the
  poster listing like other slots, so listings carry `poster` URLs without
  reads. Covers render to ContentKit's token-gated `private/` and are copied to
  `public/` unless the post is hidden: drafts start hidden, publishing one
  exposes it (`ExposeTx` in the same transaction; members-only and paid posts
  keep a public cover), and deleting a post removes its public copies first.
  `Hooks.PublicRemoved` (a CDN purge) is a no-op here.
- **Inline preview.** Videos the viewer can play preview in the feed with
  their own HLS, muted (mouse: after 500 ms of hover; touch: the most visible
  one), from the cover's frame or 10 % in (ContentKit `MediaGallery`). Locked
  posts show only the cover (`VideoPoster`): they have no stream.
- **Uploads.** Post images need `channel:posts:create`, channel slots
  `channel:settings:manage`, a user their own avatar. The UploadLimiter
  rate-limits each uploader (429) and holds each channel's quota: presign
  refuses early and a commit that would exceed it fails (413
  `quota_exceeded`, shown in the editor); site admins are exempt. Deleting a post erases its folder and releases quota;
  channel purge erases post and channel folders; account purge erases the
  avatar.
- **Delivery.** Production uses cookie mode: the site and `media.` host share a
  registrable domain (`MEDIA_COOKIE_DOMAIN`) over HTTPS. Locally the app
  (`localhost:5173`) and worker (`localhost:8090`) share no parent domain, so
  `.env.example` uses URL tokens (`MEDIA_DELIVERY=url`).

### Production media configuration

- Serve `media-access` at `media.<domain>` and use cookie delivery:
  `MEDIA_URL=https://media.<domain>`, `MEDIA_DELIVERY=cookie`,
  `MEDIA_COOKIE_DOMAIN=<domain>`.
- `MEDIA_ACCESS_CORS_ORIGINS` lists exactly the site origins
  (`https://<domain>`, plus `https://www.<domain>` if it is served);
  `MEDIA_ACCESS_HOSTS=media.<domain>`. Keep the default
  `Cross-Origin-Resource-Policy: same-site`, which blocks other sites from
  hotlinking media.
- Keep the bucket private. media-access gets a read-only key limited to
  `*/private/*` and `*/public/*`.
- A CDN may cache `public/` (immutable names); wire `Hooks.PublicRemoved` to
  purge it. Never let a shared cache store `private/`: it is token-gated.
- To rotate `MEDIA_TOKEN_KEY`, give media-access the new key as
  `MEDIA_ACCESS_TOKEN_KEY` and the old one as `_PREVIOUS`, switch the app
  to the new key, and drop the old key after about 5 hours (token TTL plus
  the rounding window).
- The read API rate-limits each viewer (ContentKit default: 2 requests/s,
  burst 120) and logs every signed response with the viewer id.

## Buying and membership

One-time purchases go from the frontend to the post purchase endpoint, then through the
portable OpenRails Client to the provider's hosted page or an in-page card form.
The host checks only content
and channel policy; OpenRails decides offer validity, repeat purchase eligibility,
accepted terms and payment state. An exact idempotency lookup happens before
mutable admission checks, so retrying an accepted attempt does not create a new
payment or lose its original terms. The UI persists the attempt key.
After a post is physically removed, its purchase endpoint returns 404; an already
accepted checkout and its financial history remain available by checkout ID.

Membership uses in-page customer card setup, a saved method, an immutable
membership quote and an explicit payer confirmation. The browser uses the native
`/billing/v1/me/checkout/:id` read/confirm routes. Generic billing checkout creation
is omitted from this profile, so it cannot bypass the app's admission rules.
A redirect is never proof of payment; the UI reads the verified session state.

`/me` shows managed channels and a paginated purchased-post library. Its Billing
tab is `@openrails/billing-ui`'s `AccountBilling` on the embedded `/billing/v1/me`
API: subscriptions (cancel with feedback, resume, change card), saved cards
(add with any PSP that supports in-page card setup, remove, default) and payment history.
Checkout success/cancel URLs return to the request's `Origin` (else
`PUBLIC_URL`); OpenRails refuses any origin outside `PUBLIC_URL` + `RETURN_ORIGINS`.

Site admins (`root:billing:operate`) run merchant operations on
`/api/v1/admin/billing`, e.g. migrating a legacy NMI book
([OpenRails runbook](https://github.com/open-rails/openrails/blob/master/docs/batch-import.md)):

| Route | OpenRails call |
| --- | --- |
| `POST /import` | `ImportBilling` (a `DeclaredBilling` book) |
| `POST /provider-refresh` | `RefreshProviders` |
| `POST /subscriptions/:id/takeover[/preview\|/abandon]`, `GET …/takeover` | `TakeOverBilling` (`Idempotency-Key`), `PreviewEngineTakeover`, `AbandonEngineTakeover`, `GetEngineTakeover` |
| `POST /takeovers` | `TakeOverBillingBatch` |
| `POST /subscriptions/:id/change-tier[/preview]` | `ChangeTier` (`Idempotency-Key`), `PreviewTierChange` |
| `POST /payments/:id/refunds` | `RefundPayment` (`Idempotency-Key`; `revoke_access` ends access) |

## Deletion and identity

Deleting a channel sets `deleted_at` and hides content immediately. AuthKit retires
its publishing group before successful acknowledgement, removing authorization
and immediately unblocking the owner's account deletion. A durable River job
archives offers, then waits until the stored deletion timestamp is at least 30 days
old before physically removing retained posts, group and channel. Retries do not
extend the deadline. Repeating DELETE on a deleted channel returns 404.

Native account deletion is recoverable for 30 days. A correct password login during
that period returns a recovery proof; explicit confirmation restores the account.
Durable identity callbacks cancel the customer's renewable billing agreements
through the OpenRails Client, preserving the already paid access period and
permanent purchases. Restoring an account does not silently restart billing.
Already-issued ordinary access tokens keep their normal fifteen-minute lifetime;
permission checks still read current role authority.

## Sign-in and security

The frontend uses `@openrails/auth-ui`: the access token stays in memory and the
refresh token is an HttpOnly cookie (`__Host-authkit_rt` on HTTPS, `authkit_rt`
on local HTTP). Email verification is optional, but AuthKit refuses new sign-in
methods (2FA, passkeys, providers, wallets) until the address is proven; the UI
then asks for the code and retries. Two-factor (authenticator app or email) is
opt-in from Account. `task seed` marks its `example.test` creators verified.
Sign-in, registration, 2FA, recovery and the Account security panels are
auth-ui's styled components; AuthKit's emailed links land on `/verify`
(`VerifyLink`) and `/reset` (`ResetPasswordForm`), OIDC on `/login/callback`.
The demo has no mail provider: verification codes, 2FA email codes and those
links are written to the server log as `[outbox]` lines.

Operator commands use the same application connection:

```sh
go run . admin grant --user-id <registered-user-id>
go run . admin revoke --user-id <registered-user-id>
```

## Build and manual walkthrough

```sh
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend lint
pnpm --dir frontend build
go build ./...
go vet ./...
SMOKE_DATABASE_URL='postgres://postgres:postgres@localhost:55433/postgres?sslmode=disable' task smoke
```

`task smoke` loads `.env` for the `MEDIA_*` bucket settings.

The optional walkthrough creates and drops only its own uniquely named local
database. It runs real HTTP, AuthKit, OpenRails and River with a closed fake Stripe
transport. It covers purchases/replay, resource access, saved-card membership,
explicit quote confirmation/cancellation and retained deletion. Unexpected provider
requests fail locally; no real Stripe request or credential is used.

`TestMediaEndToEnd` runs the media proof on real PostgreSQL, MinIO, the
ContentKit handlers, the app's media worker (libvips, ffmpeg) and the
`media-access` binary (fake Stripe only): uploads and variants, a mixed
image/video post (HLS playlists and byte ranges, downloads, locked viewers,
ceilings; skipped without ffmpeg locally), what anonymous, member, buyer and members_ppv
buyer (after membership lapse) viewers get, editor/admin bypass, the default
ladder reaching the encoder, 9:21 and 21:9 sources (rung-keyed renditions,
playlists and downloads) and a refused 3:1 one, editor-only variants and edit
data, rate and quota refusals (presign and commit), slots (upload with a crop,
rotate, re-crop, versioned srcset widths, also from a post image) and
post erasure. `task dev:up && task test:media`
runs it locally.

`TestNMIBillingEndToEnd` runs billing against OpenRails's `nmimock` NMI gateway
on loopback, with one fake clock for OpenRails and the gateway: signup, a saved
card buys a post and a membership, the membership renews, and a declined renewal
enters dunning (`past_due`). `task dev:up && task test:nmi` runs it locally; CI
runs it beside the media proof. CI runs on pull requests only, by changed paths: Go, module
or migration changes run vet, this proof and the smoke walkthrough; frontend
changes lint and build the frontend; docs run nothing. Both workflows also
run manually (`workflow_dispatch`). The libraries retain their full automated qualification. A real sandbox
purchase/subscription walkthrough is a separate deliberate activity with retained
provider receipts and idempotency keys.
