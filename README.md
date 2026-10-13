# OnlyDemo

A React/Fiber creator app: creators run channels and publish posts on them.
ContentKit owns content: channels, their offers and media. AuthKit handles
people and publishing roles; OpenRails handles purchases, recurring
memberships and payment history. The app is moving onto ContentKit's routes in
two phases; [docs/contentkit-cutover.md](docs/contentkit-cutover.md) tracks
what moved and what is left.

The application API is `/api/v1`, ContentKit's is `/contentkit/v1`, identity
JSON API is `/auth/v1`, and customer billing is `/billing/v1`. The React app lives at `/`; the searchable native route
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
| `NOTIFICATION_RECEIPTS` | Include future optional successful-charge receipts (default `false`) |
| `CONTENT_SCHEMA` | ContentKit's schema (default `contentkit`); its media worker queue is `<CONTENT_SCHEMA>_media_worker` |
| `MEDIA_S3_*` | Media bucket: `ENDPOINT`, `PUBLIC_ENDPOINT` (browser presign host), `BUCKET`, `REGION`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY` |
| `MEDIA_URL`, `MEDIA_DELIVERY`, `MEDIA_COOKIE_DOMAIN` | media-gateway origin; `cookie` (default) or `url` delivery |
| `MEDIA_TOKEN_KEY`, `MEDIA_TOKEN_KEY_PREVIOUS` | `{kid}:{base64 32+ bytes}` signing keys shared with media-gateway |

There is no separate billing database URL or billing encryption key. Credentials
are supplied as a host-owned snapshot (`merchant.yaml`). `onlydemo migrate`
applies the app's schema and the shared River tables; `authkit.New`,
`openrails.New` and `contentkit.New` create or upgrade their own, there and
when the server starts. OpenRails v1 and AuthKit v1 do not upgrade older baselines: use a new
database, preserving old databases and receipts. Startup validates checksums and
never rewrites them.

## Channels and reading

A channel is a ContentKit group (`kind=channel` in
[contentkit.yaml](contentkit.yaml)): its handle, name, cover and avatar, and its
membership offer. Its AuthKit group, with the same id, is the group's
`auth_scope`: owners manage the channel and share owner/editor roles; editors
publish. Readers, members and buyers are never added to it. A creator makes a
channel with `POST /contentkit/v1/groups`; ContentKit provisions its AuthKit
group (`adapters/authkit`) with the creator as owner and retires it with the
channel.

A post is ContentKit content of the `post` kind, owned by its channel
(`owner_group_id`), at `/p/<CODE>/<slug>`: its text, its one hidden version's
media, its access rule and its offer. Each has one of four policies:

- **Public** (`public`): free to read.
- **Membership** (`group`): open to holders of the channel's key, including future posts.
- **Members-only purchase** (`group_ppv`): bought by members.
- **One-time purchase** (`ppv`): bought by anyone signed in.

A purchase stays readable permanently, even after the post is deleted:
ContentKit keeps it for holders of its key and lists it in their library.

## Media

Images and videos use ContentKit media through the engine
(`contentkit.go`): one private bucket, an item per post, channel and user
(`onlydemo/{kind}/{id}/`). Browsers upload straight to the bucket with
`@openrails/contentkit-ui`, presigning and committing at
`/contentkit/v1/media/upload/*` and reading at `/contentkit/v1/media/*`.

- **Posts.** `images/{name}` (25 MiB, 40 per post) and `videos/{name}` (20 GiB
  multipart, 10 per post). Private `large` WebP renditions and an HLS ladder;
  public `preview`, the first image blurred. The channel's editors upload; the
  app decides who opens them until ContentKit stores posts.
- **Channels.** ContentKit's group slots: covers (3:1) and avatars, uploaded
  by holders of `channel:media:write` (owners).
- **Users.** Avatars, each uploaded by its person.
- **Worker.** `onlydemo media-worker` (`task media:worker`) is ContentKit's
  media worker with the engine's registry: it places uploads, renders images
  and encodes video from `<CONTENT_SCHEMA>_media_worker`; readiness and purges
  come back through the host River fleet. Env: `MEDIA_WORKER_*` and
  `MEDIA_HOST_RIVER_SCHEMA` (default `RIVER_SCHEMA`).
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

## Notifications

The signed-in bell and `/me?tab=notifications` use ContentKit's inbox at
`/contentkit/v1/notifications`. The host River fleet imports up to 100
pending OpenRails events every ten seconds under the stable `onlydemo-inbox`
consumer. It verifies the customer against AuthKit's directory, publishes the
notices through the engine's client, then acknowledges the source events. Retry preserves local
read/archive state; reading or archiving does not resolve a billing issue.

Successful-charge receipts default off. `NOTIFICATION_RECEIPTS=true` imports
future optional receipts; previously skipped events are not replayed. Account
purge permanently erases and fences its inbox against delayed imports. Keep the
consumer registered across restarts; retire it only when deprovisioning this
application. Drain pending events before a merchant billing archive handoff.

## Buying and membership

A channel's membership is a ContentKit group offer, set by its owner:
`PUT /contentkit/v1/groups/{id}/offer` with `{"amount": "4.99", "renew_hours":
720}` (paid, at least 1.00) or `{"amount": "0"}` (free); `DELETE` withdraws it.
ContentKit creates and archives the OpenRails product (key and entitlement:
`onlydemo:group:<id>`) and its prices. A free membership is
`POST`/`DELETE /contentkit/v1/groups/{id}/join`. A paid one is an OpenRails
order for the offer's `price_id` (`POST /billing/v1/me/orders`), paid in
billing-ui's `OrderCheckout`, including saved cards. Withdrawing an offer stops
new purchases; members and subscribers keep theirs.

Billing in Account is `@openrails/billing-ui`'s `AccountBilling` on the
embedded `/billing/v1/me` API: subscriptions, saved cards and payment history.
Return URLs name `PUBLIC_URL` or the request's `Origin`; OpenRails refuses any
origin outside `PUBLIC_URL` + `RETURN_ORIGINS`.

Site admins (the root `admin` role) are ContentKit staff (groups, offers,
comment moderation) and work on customers' billing through the OpenRails
merchant API at `/billing/v1/merchant/*`
([route table](https://github.com/open-rails/openrails/blob/master/docs/api/routes.md)).
Grant the role with `task admin:grant USER_ID=<id>`.

## Deletion and identity

Deleting a channel (`DELETE /contentkit/v1/groups/{id}`) withdraws its
membership offer, deletes its posts for everyone but their buyers and retires
its AuthKit group, removing authorization and unblocking the owner's account
deletion.

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

- `TestChannelsOnContentKit` (needs MinIO): channels as ContentKit groups,
  posts as their content, free and paid memberships and a post's offer
  through ContentKit, the OpenRails products ContentKit keeps, members-only
  posts and their media, uploads under AuthKit roles, deletion. `task dev:up && task test:channels`.
- `TestNMIBillingEndToEnd`: a saved card buys a post and a membership through
  ContentKit's offers; the purchase opens the post (and keeps it, in the
  library, after its deletion), the membership opens the channel's posts,
  renews, and a declined renewal enters dunning; billing notices reach
  ContentKit's inbox. `task dev:up && task test:nmi`.

`task seed` and `task smoke` drive a running server (`--url`, default
http://127.0.0.1:3000) through the same routes the browser uses.

CI runs on pull requests, by changed paths: Go, module or migration changes run
vet and both proofs; frontend changes lint, test and build the frontend.
