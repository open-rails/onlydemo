# OnlyDemo

A React/Fiber creator app: creators run channels and publish posts on them.
AuthKit handles people and publishing roles; OpenRails handles product offers,
purchases, recurring memberships and payment history.

The application API is `/api/v1`, identity JSON API is `/auth/v1`, and customer
billing is `/billing/v1`. The React app lives at `/`; the searchable native route
directory is `/dev/routes`. AuthKit protocol anchors stay at
`/.well-known/jwks.json` and `/oidc` when configured. The issuer stays the origin.

## Run locally

Prerequisites: Docker, Go 1.26.9, Node 24 with pnpm 11, libvips (`libvips-dev`;
the media worker's image jobs are CGO) and ffmpeg with ffprobe (its video jobs). Copy
`.env.example` to `.env` and set `MEDIA_TOKEN_KEY`
(`echo "dev:$(openssl rand -base64 32)"`), copy `merchant.example.yaml` to
`merchant.yaml` with your sandbox PSP, then:

```sh
task dev:up   # PostgreSQL (with PGroonga) and MinIO with the media bucket; idempotent
task run
task seed     # optional display channels and posts, created through the API
```

`task run` migrates (idempotent), then serves Go with Air, the frontend with
Vite, the media gateway (`task media:gateway`) and the media worker
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

Billing is required and sandbox-only. The seller and its PSPs live in
`merchant.yaml` (copy `merchant.example.yaml`; it holds secrets and is
gitignored), read with `openrails.ReadMerchantFile`. Enabling a provider is
adding it there; startup refuses a PSP whose credentials are not sandbox. The
browser receives OpenRails's browser-safe PSP config and `@openrails/billing-ui`
picks each flow (Collect.js, Stripe Elements, hosted redirect); there is no
provider-specific code here. With several PSPs, `settings.checkout_routing`
picks the one for new purchases and new cards; the others keep serving their
existing cards and subscriptions. Buyers see one payment modal: saved cards,
an inline new card (always saved), one Pay button.

| Provider | `merchant.yaml` |
| --- | --- |
| Stripe | `account_id`; secrets `secret_key` (`sk_test_`/`rk_test_`), `webhook_signing_secret`; setting `publishable_key` (`pk_test_`, for in-page card entry) |
| NMI | `account_id` (gateway ID); secrets `security_key` (test mode), `webhook_signing_secret`; settings `tokenization_key`, optional `tokenization_url`, `endpoint_deployment` (`gateway` or `sandbox`) |

Declared PSP settings seed a new database; an existing PSP row keeps its stored
settings.

The local sandbox has OpenRails's destructive-action switch armed, so terminal dunning
cancels, revocations and provider-schedule cancels execute; an operator disables them
with `UPDATE billing.destructive_action_switch SET enabled=false, updated_by='<you>', reason='<why>', updated_at=now();`.

Webhooks go to `/billing/v1/webhooks/<rail>/<account_id>`. For Stripe export
`STRIPE_SECRET_KEY` and `STRIPE_ACCOUNT_ID` (as in `merchant.yaml`), run
`task stripe:listen`, copy its `whsec_...` into `merchant.yaml` and
keep it running (it finds `stripe` on PATH, then `.runtime/bin/stripe`; restricted
keys need Debugging Tools: Write). NMI posts only to a public HTTPS URL: register
`https://<public-host>/billing/v1/webhooks/nmi/<account_id>` in the NMI portal
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
| `MERCHANT_FILE` | The merchant declaration (default `merchant.yaml`) |
| `TRUSTED_COUNTRY_HEADER` | Edge header with the buyer's country (e.g. `CF-IPCountry`) to preselect the billing country; unset = never trusted, the browser locale is used |
| `MEMBERSHIP_PERIOD` | Renewal period of membership prices set afterwards, in whole hours (default `720h`; e.g. `1h` to test rebilling) |
| `POST_DELETION_REFUND`, `POST_DELETION_REFUND_WINDOW` | Deleting a paid post: `refund` (default), `review` or `none` for one-time purchases made within the window before deletion (default `720h`). A post that becomes free refunds nothing |
| `CONTENT_SCHEMA` | ContentKit schema (default `content`): the upload limiter's counters and content codes |
| `MEDIA_S3_*` | Media bucket: `ENDPOINT`, `PUBLIC_ENDPOINT` (browser presign host), `BUCKET`, `REGION`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY` |
| `MEDIA_URL`, `MEDIA_DELIVERY`, `MEDIA_COOKIE_DOMAIN` | media-gateway origin; `cookie` (default) or `url` delivery |
| `MEDIA_TOKEN_KEY`, `MEDIA_TOKEN_KEY_PREVIOUS` | `{kid}:{base64 32+ bytes}` signing keys shared with media-gateway |
| `MEDIA_UPLOAD_FILES_PER_HOUR`, `MEDIA_UPLOAD_BYTES_PER_DAY`, `MEDIA_CHANNEL_QUOTA_BYTES` | Upload limits (defaults 60, 100 GiB, 500 GiB) |

There is no separate billing database URL or billing encryption key. Credentials
are supplied as a host-owned snapshot (`merchant.yaml`). `onlydemo migrate`
applies the app's and ContentKit's schemas and the shared River tables;
`authkit.New` and `openrails.New` create or upgrade their own when the server
starts. OpenRails v1 and AuthKit v1 do not upgrade older baselines: use a new
database, preserving old databases and receipts. Startup validates checksums and
never rewrites them.

## Channels and reading

Every post belongs to a channel. AuthKit is the only authority for publishing:
channel owners can manage the channel and share owner/editor roles; editors can
publish and edit but cannot manage owners or delete the channel. Ordinary readers,
subscribers and purchasers are **not** added to the publishing group.

`/c` lists channels, `/c/new` creates one and `/c/<handle>` is a channel. A
post is ContentKit's uniform content URL, `/p/<CODE>/<slug>`: a permanent
9-character code (ContentKit's `contenturl`, registered with `Put` when the
post is created and again on a new title) and a slug from its title. The
server reads only the code, so the slug is decoration. Before the app shell,
the router answers a missing or stale slug, another spelling of the code or a
trailing slash with a 301 to the canonical path, a deleted post with 410, and
drafts and unknown codes like any unknown page; a canonical page carries
`<link rel="canonical">`, `og:url` and `og:title` (and a `Link` header). The
API takes the code (`/api/v1/posts/<CODE>`) and returns `code` and `slug`; the
browser builds links with `@openrails/contentkit-ui/urls` and keeps the address
bar canonical. `onlydemo urls backfill` (`task urls:backfill`) registers posts
that have no code; it is idempotent.

Posts have one of four content policies:

- **Public:** free to read.
- **Membership:** included while channel membership is active, including future posts.
- **Members-only purchase:** active membership is required to start a purchase.
- **One-time purchase:** anyone signed in can buy.

A purchased post stays readable permanently, even after membership ends or the
post's access policy changes. Deleted content is hidden. Payment history remains.

The application stores content, its access policy and each channel's slug and
name (AuthKit holds only the channel's permission group, with the channel's id),
not a duplicate product, price or sales ledger. One merchant catalog holds a
product per sold resource, keyed by the ContentKit entitlement it grants:
`content:onlydemo:post:<id>` and `members:onlydemo:channel:<id>`, plus
`members:onlydemo:channel:<id>:free`, the never-sold product granting a free
member the membership. Catalog offers own native
currency amounts, availability and immutable price versions. USD offers appear
first; another displayed currency is charged only when explicitly selected.
A channel has no membership until its owner creates one (`PUT
/api/v1/channels/:id/membership` with `{"enabled": bool, "price": {...} | null}`;
`null` is free, paid is at least $1.00). Membership and members-only posts
require one. States:

- **Open, paid:** new members subscribe; repricing affects new members only.
- **Open, free:** `POST /join` grants the free membership product (no card);
  `POST /leave` revokes the grant. Making a paid membership free stops paid renewals at
  period end and grants those members the free membership.
- **Closed** (`enabled: false`): no new joins. Existing members keep access,
  including new membership posts, and keep renewing at their accepted price.
  Free members stay members when a price is set again.

Channel membership renews every `MEMBERSHIP_PERIOD` (default `720h`: **every 30 days**,
not a calendar month); a change applies to prices set afterwards. Repricing moves a stable price key and preserves already accepted terms.

## Media

Images and videos use ContentKit media: one private bucket, an item per post,
channel and user (`onlydemo/{kind}/{id}/`), declared once in the registry
(`mediaKinds` in media.go). Browsers upload straight to the bucket with
`@openrails/contentkit-ui` (npm, the ContentKit version). ContentKit's HTTP API
is one mount, `/api/v1/contentkit`: the app presigns and commits
(`/media/upload/*`), reads (`/media/*`) and resolves post codes (`/codes/*`).
Posts are the app's own, so the content module there registers no kinds.

- **Posts.** Uploads `images/{name}` (JPEG, PNG, WebP, GIF, AVIF; 25 MiB, 40
  per post) and `videos/{name}` (MP4, WebM, MOV, MKV; 20 GiB multipart, 10 per
  post). ContentKit orders each path, so a post shows its images, then its
  videos. Private: `large` WebP renditions and an HLS ladder per video
  (ContentKit's default rungs and 1:2.4–2.4:1 bounds; editors see failures).
  Public: `preview`, the first image blurred, for everyone who can see the
  post; everything else is private, all or nothing per viewer.
- **Channels and users.** Public `avatar` (1:1, 128/512 px) and channel
  `cover` (3:1, 900/3000 px, from 600 px wide); the API returns each as the
  package's `PublicPreset`, read from the item's manifest: its published
  renditions (generation-suffixed names), none before an upload, when the UI
  shows its placeholder.
  The package crops before saving (`AvatarUpload`, `SlotEditor`).
- **Edits.** Crop and rotate are non-destructive `edit` ops on the kept
  original; the post cropper draws the upload's editor view.
- **Access.** One resolver for reads, playlists and uploads: ContentKit's
  `access.Gate` over OpenRails (`contentkit/adapters/openrails`), with the
  post's rule (public, members, ppv, members_ppv over the channel) and
  ContentKit's keys: `content:onlydemo:post:<id>` (owning the post) and
  `members:onlydemo:channel:<id>`, the catalog's product keys. Channel editors
  and site admins read everything; drafts are their author's, publishing posts
  their channel editors'. A page of posts costs one entitlement read.
- **Worker.** `onlydemo media-worker` (`task media:worker`) is ContentKit's
  media worker with this registry: it places uploads, renders images and
  encodes video from its own River schema `onlydemo_media_worker`; readiness
  and purges come back through the app's River queue. A publishing post goes
  live when its media is ready (`ItemReady`), its preview exposed in the same
  transaction. Env: `MEDIA_WORKER_*` (ContentKit's tuning: threads, codecs,
  concurrency, encoder) and `MEDIA_HOST_RIVER_SCHEMA` (default
  `RIVER_SCHEMA`).
- **Composer.** The first file creates the post as a draft (hidden, its
  author's only), files commit as they finish and Publish turns the draft into
  the post. Cancel deletes the draft with its folder; drafts abandoned for
  24 h are swept hourly.
- **Uploads.** Post media needs `channel:posts:create`, channel images
  `channel:settings:manage`, a user their own avatar. The upload limiter
  rate-limits each uploader (429) and holds each channel's quota (413
  `quota_exceeded`); site admins are exempt. Deleting a post erases its folder
  (ContentKit keeps a deletion tombstone, `manifest.json`) and releases quota;
  channel and account purges erase theirs.
- **Maintenance.** `onlydemo media reprocess` redoes stale outputs after a
  preset change; `onlydemo media sweep-orphans [--grace 24h] [--delete]`
  reports or removes folders with no row.
- **Delivery.** Production uses cookie mode: the site and `media.` host share a
  registrable domain (`MEDIA_COOKIE_DOMAIN`) over HTTPS. Locally the app
  (`localhost:5173`) and gateway (`localhost:8090`) share no parent domain, so
  `.env.example` uses URL tokens (`MEDIA_DELIVERY=url`).

### Production media configuration

- Serve `media-gateway` at `media.<domain>` and use cookie delivery:
  `MEDIA_URL=https://media.<domain>`, `MEDIA_DELIVERY=cookie`,
  `MEDIA_COOKIE_DOMAIN=<domain>`.
- `MEDIA_GATEWAY_HOSTS=media.<domain>=onlydemo`; `MEDIA_GATEWAY_CORS_ORIGINS`
  lists exactly the site origins.
- Keep the bucket private. The gateway gets a read-only key limited to
  `*/private/*` and `*/public/*`.
- A CDN may cache `public/`; never let a shared cache store `private/`.
- To rotate `MEDIA_TOKEN_KEY`, give the gateway the new key as
  `MEDIA_GATEWAY_TOKEN_KEY` and the old one as `_PREVIOUS`, switch the app
  to the new key, and drop the old key after about 5 hours.

## Buying and membership

A purchase or a membership starts at the app: `POST /api/v1/posts/:code/checkout`
or `POST /api/v1/channels/:id/subscribe` with `{price_id}`. The app checks its
content and channel policy (members-only purchases need a membership, editors
buy nothing, the price must be one of the offers it put on sale), then mints the
buyer's checkout session with the Go client's `CreateCheckoutSession`. The
browser pays it in `@openrails/billing-ui`'s `CheckoutModal` on the customer
surface (`/billing/v1/me/checkout-sessions/:id`), where saved cards pay too.
OpenRails decides offer validity, repeat purchase eligibility, accepted terms
and payment state; a redirect is never proof of payment, the modal reads the
verified session. Customers cannot start a checkout of their own
(`Customers: CustomerBillingManagement`), so nothing bypasses the app's rules.
After a post is physically removed, its purchase endpoint returns 404; an
already paid session and its financial history remain.

`/me` shows managed channels and a paginated purchased-post library. Its Billing
tab is `@openrails/billing-ui`'s `AccountBilling` on the embedded `/billing/v1/me`
API: subscriptions (cancel with feedback, resume, change card), saved cards
(add with any PSP that supports in-page card setup, remove) and payment history.
Return URLs name `PUBLIC_URL` or the request's `Origin`; OpenRails refuses any
origin outside `PUBLIC_URL` + `RETURN_ORIGINS`.

Site admins (the root `admin` role: `root:customers:read`, `root:customers:update`)
work on customers' billing through the OpenRails merchant API at
`/billing/v1/merchant/*`, guarded by those permissions (`Routes.Guards`
`StaffReads`, `StaffWrites`): billing import
([runbook](https://github.com/open-rails/openrails/blob/master/docs/batch-import.md)),
provider refresh, tier changes, refunds and the rest of the
[route table](https://github.com/open-rails/openrails/blob/master/docs/api/routes.md).
Grant the role with `task admin:grant USER_ID=<id>`.

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

`TestMediaEndToEnd` runs the media and URL proof on real PostgreSQL, MinIO,
the ContentKit handlers, the app's media worker (libvips, ffmpeg), the
`media-gateway` binary and the app's URL router (fake Stripe only): uploads and
private WebP renditions, a mixed image/video post (HLS playlists and byte
ranges, locked viewers, per-path ceilings), what anonymous, member, buyer and
members_ppv buyer (after membership lapse) viewers get (everything, or the
public blurred preview), editor/admin bypass, editor-only edits, rate and quota
refusals (presign and commit), public avatars and covers (crop, rotate,
re-crop), drafts, publishing after processing, content URLs (codes, canonical
redirects and tags, 410 after deletion, the backfill), listing cost and post
erasure. `task dev:up && task test:media` runs it locally. `pnpm --dir frontend
test` checks the browser's post links.

`TestNMIBillingEndToEnd` runs billing against OpenRails's `nmimock` NMI gateway
on loopback, with one fake clock for OpenRails and the gateway: signup, a saved
card buys a post and a membership, the membership renews, and a declined renewal
enters dunning (`past_due`). It needs only PostgreSQL (media's startup probe
uses an in-process S3). `task dev:up && task test:nmi` runs it locally; CI runs
it before the media proof. CI runs on pull requests only, by changed paths: Go, module
or migration changes run vet, this proof and the smoke walkthrough; frontend
changes lint and build the frontend; docs run nothing. Both workflows also
run manually (`workflow_dispatch`). The libraries retain their full automated qualification. A real sandbox
purchase/subscription walkthrough is a separate deliberate activity with retained
provider receipts and idempotency keys.
