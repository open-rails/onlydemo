# OnlyDemo

A React/Fiber creator app: creators run channels and publish posts on them.
ContentKit 0.126 owns content, channel entities and media. AuthKit handles
people and publishing roles; OpenRails owns products, prices, purchases,
memberships and payment history. [The integration guide](docs/contentkit-cutover.md)
describes the ownership boundaries and upgrade procedure.

The application API is `/api/v1`, ContentKit's is `/contentkit/v1`, identity
JSON API is `/auth/v1`, and customer billing is `/billing/v1`. The React app lives at `/`; the searchable native route
directory is `/dev/routes`. AuthKit protocol anchors stay at
`/.well-known/jwks.json` and `/oidc` when configured. The issuer stays the origin.

## Run locally

Prerequisites: Docker, Go 1.26.10, Node 24 with pnpm 11, libvips (`libvips-dev`;
the media worker's image jobs are CGO) and ffmpeg with ffprobe (its video jobs). Copy
`.env.example` to `.env`, set a permanent `AUTH_TENANT_ID` (`uuidgen`), and set `MEDIA_TOKEN_KEY`
(`echo "dev:$(openssl rand -base64 32)"`), copy `merchant.example.yaml` to
`merchant.yaml` with your sandbox PSP, then:

```sh
task dev:up   # PostgreSQL (with PGroonga) and MinIO with the media bucket; idempotent
task run
task seed     # optional display channels and posts, through ContentKit's routes
task smoke    # walks a creator and a fan through the running server
```

`task run` migrates (idempotent), then serves Go with Air, the frontend with
Vite, the media gateway (`task media:gateway`) and the media worker
(`task media:worker`, `onlydemo media-worker`), reloading on change; open http://localhost:5173.
The MinIO console is http://localhost:59001 (`onlydemo` / `onlydemo-dev-secret`). `task run:embedded` builds the frontend into
the single Go binary and serves everything from http://localhost:3000.
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
adding it there. Each PSP declares its `environment` (`test` or `sandbox`;
startup refuses `live`), and OpenRails verifies it with the provider. The
browser receives OpenRails's browser-safe PSP config and `@openrails/billing-ui`
picks each flow (Collect.js, Stripe Elements, hosted redirect); there is no
provider-specific code here. With several PSPs, `settings.checkout_routing`
picks the one for new purchases and new cards; the others keep serving their
existing cards and subscriptions. Buyers see one payment modal: saved cards,
an inline new card (always saved), one Pay button.

| Provider | `merchant.yaml` |
| --- | --- |
| Stripe | `environment: test`, `account_id`; secrets `secret_key` (`sk_test_`/`rk_test_`), `webhook_signing_secret`; setting `publishable_key` (`pk_test_`, for in-page card entry) |
| NMI | `environment` (`test` for a gateway account in test mode, `sandbox` for sandbox.nmi.com), `gateway_id` (gateway ID); secrets `security_key`, `webhook_signing_secret`; settings `tokenization_key`, optional `tokenization_url` |

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
| `AUTH_TENANT_ID` | Persisted account tenant UUID; retain it with the database and backups |
| `AUTH_ISSUER`, `AUTH_AUDIENCE` | Token issuer/audience; origin issuer matches root discovery |
| `AUTH_KEYS_PATH` | Dev signing and TOTP key directory (default `.runtime/auth`), so sessions and authenticator apps survive restarts |
| `AUTH_SCHEMA`, `BILLING_SCHEMA`, `RIVER_SCHEMA` | Optional independent schema names; shared `public` is supported |
| `MERCHANT_FILE` | The merchant declaration (default `merchant.yaml`) |
| `TRUSTED_COUNTRY_HEADER` | Edge header with the buyer's country (e.g. `CF-IPCountry`) to preselect the billing country; unset = never trusted, the browser locale is used |
| `CONTENT_SCHEMA` | ContentKit's schema (default `contentkit`); its media worker queue is `<CONTENT_SCHEMA>_media_worker` |
| `MEDIA_S3_*` | Media bucket: `ENDPOINT`, `PUBLIC_ENDPOINT` (browser presign host), `BUCKET`, `REGION`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY` |
| `MEDIA_URL`, `MEDIA_DELIVERY`, `MEDIA_COOKIE_DOMAIN` | media-gateway origin; `cookie` (default) or `url` delivery |
| `MEDIA_TOKEN_KEY`, `MEDIA_TOKEN_KEY_PREVIOUS` | `{kid}:{base64 32+ bytes}` signing keys shared with media-gateway |

There is no separate billing database URL or billing encryption key. Credentials
are supplied as a host-owned snapshot (`merchant.yaml`). `onlydemo migrate`
applies the shared River tables and the host account-event cursor; `authkit.New`,
`openrails.New` and `contentkit.New` create or upgrade their own, there and
when the server starts. OpenRails v1 and AuthKit v1 do not upgrade older baselines: use a new
database, preserving old databases and receipts. Startup validates checksums and
never rewrites them.

## Channels and reading

A channel is a ContentKit entity (`kind=channel`, `entitled: true` in
[contentkit.yaml](contentkit.yaml)) at `/en/c/<CODE>/<name-slug>`. Its AuthKit
permission group has the same ID. Owners manage the channel and its catalog;
editors publish posts. Readers and buyers never join the editorial group.
`POST /contentkit/v1/entities` creates the channel and provisions its owner.

A post is ContentKit content (`kind=post`) with one hidden media version,
owned by its channel. Canonical URLs are `/en/p/<CODE>/<slug>`.
The existing wire fields and entitlement keys retain their identity:
`owner_group_id`, `owner_group`, `group_id`, and `onlydemo:group:<id>`.
The Go and React APIs call these entities.

Access rules are public, channel membership (`group`), members-only purchase
(`group_ppv`), or one-time purchase (`ppv`). ContentKit checks the customer's
current OpenRails keys; it does not create products or prices.

Deleting the final version removes the post. It can be restored for 30 days,
buyers retain access during that window. ContentKit's existing purge sweep
then removes its media and database rows. Billing history and grants remain in OpenRails independently.

## Media

Images and videos use ContentKit media through the engine
(`contentkit.go`): one bucket, media owned by each post version, channel and user.
Post media uses the version's storage ID; its parent post is database metadata.
Browsers upload straight to the bucket with
`@openrails/contentkit-ui`, presigning and committing at
`/contentkit/v1/media/upload/*` and reading at `/contentkit/v1/media/*`.

- **Posts.** `images/{name}` (25 MiB, 40 per post) and `videos/{name}` (20 GiB
  multipart, 10 per post). Private `large` WebP renditions and an HLS ladder;
  public `preview`, the first image blurred. The channel's editors upload; the
  app decides who opens them through ContentKit's current access decision.
- **Channels.** ContentKit's entity slots: covers (3:1) and avatars, uploaded
  by holders of `channel:media:write` (owners).
- **Users.** Avatars, each uploaded by its person. Until then the gateway
  serves a default: `onlydemo media-defaults` renders it into the bucket and
  prints `MEDIA_GATEWAY_DEFAULTS` (`task media:gateway` runs it).
- **Worker.** `onlydemo media-worker` (`task media:worker`) is ContentKit's
  media worker with the engine's registry. Its three processing jobs ingest
  committed uploads, render images and encode video from
  `<CONTENT_SCHEMA>_media_worker`. Processors append progress facts directly;
  the shared editor observes durable state through authenticated SSE and
  recovers it when reopened. Uploading does not publish a post. The host fleet
  runs metadata work and the existing content-purge sweep; there are no separate
  readiness, exposure or media-deletion workers. Env: `MEDIA_WORKER_*` and
  `MEDIA_HOST_RIVER_SCHEMA` (default `RIVER_SCHEMA`).
- **Access.** `/public` is always public, including previews before publication.
  `/private` requires a scoped token. Publishing, unpublishing and changing
  access rules do not move or regenerate media.
- **Delivery.** Production uses cookie mode: the site and `media.` host share a
  registrable domain (`MEDIA_COOKIE_DOMAIN`) over HTTPS. Locally the app
  (`localhost:5173`) and gateway (`localhost:8090`) share no parent domain, so
  `.env.example` uses URL tokens (`MEDIA_DELIVERY=url`).

### Production media configuration

- Serve `media-gateway` at `media.<domain>` and use cookie delivery:
  `MEDIA_URL=https://media.<domain>`, `MEDIA_DELIVERY=cookie`,
  `MEDIA_COOKIE_DOMAIN=<domain>`.
- `MEDIA_GATEWAY_HOSTS=media.<domain>=onlydemo`, `MEDIA_GATEWAY_BUCKETS=onlydemo=<bucket>`;
  `MEDIA_GATEWAY_CORS_ORIGINS` lists exactly the site origins;
  `MEDIA_GATEWAY_DEFAULTS` is what `onlydemo media-defaults` prints on each deploy.
- Keep the bucket private. The gateway gets a read-only key limited to
  `*/private/*` and `*/public/*`.
- A CDN may cache `public/`; never let a shared cache store `private/`.
- To rotate `MEDIA_TOKEN_KEY`, give the gateway the new key as
  `MEDIA_GATEWAY_TOKEN_KEY` and the old one as `_PREVIOUS`, switch the app
  to the new key, and drop the old key after about 5 hours.

## Notifications

The bell and Account inbox use `/contentkit/v1/notifications`. ContentKit's
`contentkit_openrails_inbox_sync` worker reads OpenRails's retained event log,
verifies shared UUIDs and the configured AuthKit issuer, then commits inbox
receipts and its cursor together. It registers no source consumer and sends
no ACK. The standard worker imports actionable billing lifecycle notices;
optional successful-charge receipts are not imported. Local read/archive
state survives replays and account purge fences delayed deliveries.

## Buying and membership

Creators edit prices through `/api/v1/products/{entities|contents}/{id}`.
These routes check their live channel owner permission and call native
OpenRails products and prices. `GET /api/v1/products?key=<entitlement>` returns
the product from OpenRails. Channel memberships are free or recur every 720
hours; posts are one-time purchases. Free joins and leaves at
`/api/v1/memberships/{id}` create or revoke only the caller's demo free grants.

Paywall and MembershipBox pass the required key to the host payment modal.
It reads OpenRails's current product and price, creates an order through
`/billing/v1/me/orders`, and pays with billing-ui's `OrderPayment`. Withdrawing
a product stops new sales. Changing a channel to free archives paid prices;
existing subscriptions keep their accepted terms and can be canceled in Billing.

Account billing is billing-ui's `AccountBilling`. The Purchased tab reads
OpenRails's customer entitlements and links to the corresponding content.
Payment history remains available even when the content has been purged.
Return URLs must match `PUBLIC_URL` or `RETURN_ORIGINS`.

Site admins hold ContentKit staff permissions and OpenRails customer staff
permissions. Grant the root role with `task admin:grant USER_ID=<id>`.
Channel ownership remains separate from site-wide administration.

## Deletion and identity

The channel delete UI archives its membership product, then deletes its
ContentKit entity and retires editorial permissions. This does not cancel
subscriptions or refund purchases. Post deletion archives its product before
removing the editions. Direct ContentKit API callers must manage their own
OpenRails catalog lifecycle as well.

Account deletion is recoverable for 30 days. The host's
`onlydemo_account_events` worker reads AuthKit's retained log every ten seconds;
startup catches up only when overdue. It cancels renewable billing agreements
after deletion, preserving paid
access periods and permanent billing grants. Its cursor lives in the River
schema's `onlydemo_auth_cursor` table and advances only after processing.
Cancellation is idempotent across retries. An expired cursor fails explicitly
and requires reconciliation with current account state before resetting it.
AuthKit's required `OnPurge` hook separately erases ContentKit subjects.
Restoring an account does not restart billing. Ordinary access tokens keep
their fifteen-minute lifetime; permission checks read live role authority.

## Sign-in and security

The frontend uses `@openrails/auth-ui`: the access token stays in memory and the
refresh token is an HttpOnly cookie (`__Host-authkit_rt` on HTTPS, `authkit_rt`
on local HTTP). Email verification is optional, but AuthKit refuses new sign-in
methods (2FA, passkeys, providers, wallets) until the address is proven; the UI
then asks for the code and retries. Two-factor (authenticator app or email) is
opt-in from Account.
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

## Build and tests

```sh
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend lint
pnpm --dir frontend build
go build ./...
go vet ./...
```

The integration tests run on real PostgreSQL (each test in schemas of its
own), AuthKit, OpenRails, ContentKit and River, with OpenRails's `nmimock`
gateway on loopback:

- `TestChannelsOnContentKit` (needs MinIO): entity ownership, editor permissions,
  free membership grants, protected content and media, native catalog prices,
  and deletion. `task dev:up && task test:channels`.
- `TestNMIBillingEndToEnd`: native product/price purchases, NMI sandbox payment,
  membership renewal and decline, retained billing access and ContentKit's
  destination-owned inbox receipts. `task dev:up && task test:nmi`.

`task seed` and `task smoke` drive a running server (`--url`, default
http://127.0.0.1:3000) through the same routes the browser uses.

CI runs on pull requests, by changed paths: Go, module or migration changes run
vet and both proofs; frontend changes lint, test and build the frontend.
