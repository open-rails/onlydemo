# OpenRails demo

A Go/Fiber blog API with AuthKit registration, login and admin permissions, and
OpenRails billing through Stripe's test environment. Authors can sell private
posts for a one-time USD payment. OpenRails owns checkout, payment confirmation
and access grants; the app owns posts and their catalog references.

The current integration pins published OpenRails release candidates while its
final release checks finish. All dependencies resolve through ordinary Go modules;
no local workspace or `replace` directive is needed.

## Run locally

Install Go 1.26.6+, Docker, [Task](https://taskfile.dev) and the
[Stripe CLI](https://docs.stripe.com/stripe-cli). Then:

```sh
cp -n .env.example .env
task db:up
task migrate
```

Fill in the ignored `.env` with your Stripe **test** secret key and account ID.
The host supplies these credentials directly to OpenRails; provider credential
API routes are not mounted and the keys are not copied into its database secret store.
This Stripe-only demo does not need a billing encryption key. Live Stripe keys
are rejected. Restricted `rk_test_` keys need permissions for the catalog,
customers, checkout, payments and webhook operations used by OpenRails.

In a separate terminal, start forwarding Stripe events:

```sh
task stripe:listen
```

Copy the displayed `whsec_...` value into `STRIPE_WEBHOOK_SECRET` in `.env`,
then run `task run`. Keep the listener running. Restricted keys additionally
need **Debugging Tools: Write** to use `stripe listen`. The webhook URL is
`/billing/v1/webhooks/stripe/<STRIPE_ACCOUNT_ID>`.

Open [localhost:3000](http://localhost:3000/) for a searchable route directory.
It reads Fiber's live route table, including the configured native AuthKit and OpenRails
route bundles. There is no manually maintained endpoint list in the HTML.
The homepage is an API directory; use the API examples below to create posts
and open the returned Stripe Checkout URL to pay with a
[Stripe test card](https://docs.stripe.com/testing).

Billing is required. Startup refuses missing Stripe test credentials, account or
webhook configuration. `GET /health` checks both the database and billing runtime.

PostgreSQL uses port `55433`; the API uses `3000`. Task loads `.env`; plain
`go run .` reads exported environment variables through Koanf. Configuration:

| Variable | Purpose |
| --- | --- |
| `PORT`, `PUBLIC_URL` | Listening port and browser return origin; defaults to localhost |
| `DATABASE_URL` | One owning connection/pool for initialization, blog, AuthKit, OpenRails, and River |
| `APP_SCHEMA`, `AUTH_SCHEMA`, `BILLING_SCHEMA`, `RIVER_SCHEMA` | Optional namespaces; defaults are `demo`, `profiles`, `billing`, `public` |
| `AUTH_ISSUER`, `AUTH_AUDIENCE` | AuthKit token identity |
| `STRIPE_SECRET_KEY`, `STRIPE_ACCOUNT_ID`, `STRIPE_WEBHOOK_SECRET` | Test account and webhook credentials |

The default database URL is
`postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable`.
AuthKit defaults to `profiles`, OpenRails to `billing`, and River to `public`.
All four may use `public`, or another shared schema. For example, set
`APP_SCHEMA=public AUTH_SCHEMA=public BILLING_SCHEMA=public RIVER_SCHEMA=public`. Each component addresses its
own tables explicitly; the host pool search path is unchanged. Schema changes
select a different namespace and do not move existing data. The application owns
its blog tables. The app loads only its own migrations. AuthKit
and OpenRails initialize their storage through public library calls; their SQL,
ledger keys and migration runners are private implementation details.
`task run` explicitly calls those initializers before constructing the services.
`task migrate` (`go run . migrate`) performs the same initialization and exits.
The host uses `riverkit.ApplyMigrations` for River storage and composes the
application, AuthKit and OpenRails contributions into one River client.
`go run .` and `go run . serve` start the server. Cobra provides help for every
command: `go run . help`, `go run . migrate --help`, and
`go run . admin grant --help` all work without loading configuration or connecting
to PostgreSQL.
One-off operations are CLI commands, not environment flags. Both use `DATABASE_URL`;
there is no separate admin connection, login creation, or grant script in the app.
The connected user owns the objects it creates and already has access to them.
The libraries also support optional separate runtime credentials for deployments
that want them, but this demo keeps one pool.

AuthKit and OpenRails use the same host boundary: a local runtime owns resources,
HTTP configuration and background jobs; its client exposes typed business calls.
`auth.go` and `billing.go` retain those separately. After provisioning and River
composition, `main.go` obtains `authkitfiber.Routes(auth.runtime)` and
`openrailsfiber.Routes(billing.runtime)` and mounts each bundle once. OpenRails
selects its provider callbacks from its configuration; the demo has no separate
webhook handler or handwritten callback registrations. Application routes use
`/api/v1`, AuthKit's JSON API uses `/auth/v1`, and billing uses `/billing/v1`.
AuthKit's protocol endpoints retain their canonical anchors: JWKS at
`/.well-known/jwks.json` and browser OIDC under `/oidc` when configured. The token
issuer remains the origin so discovery and signed request verification stay aligned.
The billing runtime uses host-owned snapshot credentials and explicit sandbox/write
policies. `PUBLIC_URL` supplies checkout success and cancellation destinations from
the application handler; it is not a billing API URL or a runtime dependency.
Remote OpenRails consumers only need its remote client, with no local runtime or
route bundle. AuthKit's client boundary permits a future remote transport; this
demo uses its supported embedded runtime.

This demo chooses one host-owned River fleet. `migrations.go` calls
`riverkit.ApplyMigrations` to initialize its schema. After services and merchant
configuration are ready, `jobs.go` calls `riverkit.New` with AuthKit, channel and
OpenRails contributions. It returns one unstarted client with all producers
already bound; every demo instance includes billing workers.
The host starts and stops workers before closing library services, then closes
its pool. OpenRails and River borrow that pool.
AuthKit creates and owns a schema-bound pool from the same connection settings;
closing it leaves the host pool open. A `MaxConns=1` host pool therefore does not
limit the whole process to one database connection.
Every replica must register the same complete schedules, since River's
elected leader schedules periodic jobs. A consumer that wants a library-managed
fleet can omit the host integration and let the library initialize its queue.

The local container provides the default `postgres` login. A normal PostgreSQL
login that owns its database also works; the manual walkthrough creates its own database. There
are no per-library logins or permission-group roles. Merchant authorization uses
explicit scoped queries, independently of database-role flags.

The demo and OpenRails use fresh pre-v1 schema baselines. When upgrading from an
older baseline, point `DATABASE_URL` at a new database to preserve the old one;
this release has no legacy billing upgrade or backfill path. Startup verifies
migration checksums and never rewrites historical checksums or drops databases.
AuthKit and River retain their own migration ownership.
Channel and author references are opaque AuthKit IDs, with no foreign key into AuthKit's schema.
`task db:down` removes the disposable development container and its databases.

AuthKit uses development signing keys and memory-backed challenge/rate-limit
storage. Email verification and MFA are disabled for this demo. Signing keys
change on restart, so sign in again after restarting the app.
AuthKit schedules PostgreSQL maintenance automatically through the shared River
fleet. Account deletion is recoverable for 30 days; AuthKit owns the durable
deletion jobs and final identity cleanup. Its in-memory TTL caches retain local
sweepers. The demo stores no additional account profile requiring a deletion
callback: transferred channel content retains its opaque `author_id` attribution,
and OpenRails retains financial history independently of the identity record.

## Users and admins

Register with `POST /auth/v1/register`:

```json
{"identifier":"writer@example.com","username":"writer","password":"a long example password"}
```

The response includes `token_set.access_token`. Login at
`POST /auth/v1/password/login` using `identifier` and `password`; that response
includes `access_token`. Send `Authorization: Bearer <access_token>` on
authenticated requests. `GET /auth/v1/me` provides the current user's ID.

`DELETE /auth/v1/user` deletes the account after fresh authentication (or a
`password` in the request body). First transfer or delete every channel it owns.
During the 30-day recovery period, a correct password login returns 409 with
`error.metadata.recovery.token` instead of a session. Explicitly confirm with
`POST /auth/v1/account/recovery/confirm` and `{"token":"<recovery-token>"}`, then
log in again. Ordinary login never silently restores the account; recovery
tokens are single-use and cannot authenticate normal API requests.

Grant or revoke an existing user's admin role explicitly:

```sh
task admin:grant USER_ID=<user-uuid>
task admin:revoke USER_ID=<user-uuid>
```

These operator commands use AuthKit's trusted `OperatorAssignGroupRole` and
`OperatorUnassignGroupRole` client operations. Ordinary server startup
does not restore revoked roles. The `admin` role is an AuthKit root permission
group role granting `root:posts:read`, `root:posts:edit` and `root:posts:delete`.
The application checks those permissions through AuthKit on each request;
there is no custom admin flag/table and no trust in token-carried role names.
AuthKit's root owner also has these permissions through `root:*`.

Admins may read, edit or delete any post through the same routes as authors.
Channel owners and editors manage their channel's posts. Writes use `Required` and
require a local user; reads use `Optional`. Access tokens are verified without
a per-request account-status lookup, including for moderation. Bans prevent new
login and token refresh, while issued tokens remain usable until their 15-minute
expiry. `RequiredLive` is available when an application explicitly opts into
immediate account-status checks. Group and moderation permissions are checked
through AuthKit on each request, so role revocation takes effect immediately.

## Posts and purchases

The application is one OpenRails merchant with one Stripe collection account.
Each collaborative channel is an AuthKit permission group whose immutable ID
owns an OpenRails catalog. AuthKit owns its name, slug, owners, editors, invitations
and permissions. The app stores channel lifecycle state and content; it has no
parallel ACL or owner flag. `author_id` records who created a post, while
`channel_id` determines authority and catalog scope.

Create a channel with `POST /api/v1/channels` and `{"slug":"our-channel","name":"Our channel"}`.
The authenticated creator becomes its owner. `GET /api/v1/channels/:id` reads it;
AuthKit's native `/auth/v1/channel/:slug` routes manage members, roles, invitations
and settings. Native channel creation/deletion is disabled so it cannot bypass
the app's coordinated lifecycle. A channel must retain a valid owner; transfer
ownership or delete it before deleting its owner's account.
The demo's content/channel routes require native user access tokens. AuthKit's
group API also supports remote-application owners managing or transferring roles.

`DELETE /api/v1/channels/:id` returns 202 after atomically marking deletion and
enqueuing a River job. That job archives catalog products, removes posts, and
removes the AuthKit group and application metadata. Retries are durable; payment
history and purchased-access records remain in OpenRails. No automatic refund
or provider subscription cancellation is performed.

Channel writes use `client.ForCatalogOwner(channelID)` after a live AuthKit
permission check. Root moderation uses the merchant client while preserving the
channel catalog. Creator payouts and Stripe Connect are separate features.

The demo explicitly enables `AllowCatalogUpdates` for these dynamic offers.
OpenRails reads the active catalog from its database; the demo needs no catalog
bootstrap YAML. This permission is independent of host-owned Stripe credentials
and retains the same channel and merchant authorization checks.

Catalog/provider work runs outside blog database transactions so a shared pool
cannot deadlock waiting for itself. Post updates use an atomic revision check;
concurrent edits can return 409 and should be retried after fetching the post.
Checkout captures the selected immutable offer, so a later price change does not
change an already-created checkout's terms. The billing product label keeps the
title from its first listing. Later title edits change the blog post only, so a
rejected concurrent edit cannot leave its title in billing.

| Method | Route | Behavior |
| --- | --- | --- |
| POST | `/api/v1/channels` | Create a channel and its AuthKit permission group |
| GET | `/api/v1/channels/:id` | Read an accessible channel |
| DELETE | `/api/v1/channels/:id` | Authorize through channel settings permission and queue durable cleanup |
| GET | `/api/v1/posts` | One page of public posts, sale previews, and accessible private posts (`limit=1..100`, default 50) |
| GET | `/api/v1/posts/:id` | Full content if allowed; a sale preview otherwise |
| POST | `/api/v1/posts` | Create a post in an authorized channel; record the author |
| PATCH | `/api/v1/posts/:id` | Channel owner/editor or root moderator edits |
| DELETE | `/api/v1/posts/:id` | Channel owner/editor or root moderator deletes |
| POST | `/api/v1/posts/:id/checkout` | Start one-time Stripe Checkout; `Idempotency-Key` required |
| GET | `/api/v1/checkouts/:id` | Read only the authenticated buyer's checkout |

OpenRails also exposes its native customer billing group under `/billing/v1/me`.
`billing.go` supplies native AuthKit identity and the customer route policy to the
OpenRails constructor. The library resolves the declared merchant slug and owns
the customer authorization; the app mounts the configured bundle under `/billing`.
Purchased products use cursor pagination; payments, subscriptions and invoices
have bounded pages. The group includes saved payment methods, payment recovery,
subscription cancellation/resumption and existing-agreement management. The
verified AuthKit user and configured merchant determine all ownership; request
fields cannot select another customer. New checkout uses the application's
selected-offer path; plan changes are not exposed. `/billing/v1/capabilities` describes the
mounted route groups and provider-specific `features`.

Create a paid post:

```json
{
  "channel_id": "<channel-group-uuid>",
  "slug": "my-paid-article",
  "title": "My paid article",
  "body": "The complete article is visible after purchase.",
  "visibility": "private",
  "price_cents": 499
}
```

Amounts are integer USD cents: `499` means **$4.99**. This demo accepts
50–99,999,999 cents. A public post cannot have a price. Omit `price_cents` for
an unlisted private post; PATCH with `price_cents: 0` removes a sale listing.
PATCH omission leaves the price unchanged. `channel_id` is required on creation
and cannot be changed by PATCH. Authority always comes from AuthKit.

Sale previews include title, channel, author, price and `can_read: false`, but omit the
body. Unlisted private posts return 404 to unauthorized viewers. Channel readers and
admins can read them; buyers can read purchased posts even after repricing or
delisting. A purchase does not grant editing rights. Deleting the post removes
the content, while OpenRails retains its billing records.

As a different user, request checkout:

```sh
curl -X POST http://localhost:3000/api/v1/posts/1/checkout \
  -H "Authorization: Bearer $BUYER_TOKEN" \
  -H 'Idempotency-Key: purchase-attempt-1'
```

Open the response's `url` in a browser. Retry the same purchase attempt with
the same key; use a new key for a new attempt. The server chooses the current
price, currency and buyer. OpenRails keeps the catalog locally and passes the
accepted one-time price inline to Stripe Checkout; it does not mirror Products
or Prices into Stripe. Supplying those fields in the request cannot change
the charge. Checkout status is scoped to its buyer.

Access is granted only after OpenRails confirms payment through Stripe's
signed webhook pipeline. A success redirect is not proof of payment, and
unpaid, expired or forged events grant no access. Repeated delivery does not
duplicate the grant. Prices do not renew and access has no expiration; refunds
and disputes remain subject to OpenRails' billing/access policy.

This demo uses one Stripe storefront. Authorized channel publishers choose prices; separate seller
accounts, commissions and payouts are outside this example.

Direct admin commands use the same application environment:

```sh
go run . admin grant --user-id <registered-user-id>
go run . admin revoke --user-id <registered-user-id>
```

## Build and manual walkthrough

```sh
go build ./...
go vet ./...
SMOKE_DATABASE_URL='postgres://postgres:postgres@localhost:55433/postgres?sslmode=disable' scripts/smoke.sh
```

The optional manual walkthrough creates and removes its own fresh local database.
It exercises real native registration/login, channel/post creation, catalog checkout,
idempotent replay, a signed fake paid webhook, purchased access and billing history.
Its Stripe transport handles requests locally and refuses unexpected requests;
no real provider credentials or network writes are used. PostgreSQL must be on a
loopback address and the supplied login must be allowed to create databases.

This demo has no automated test suite. CI builds and vets the application; the
libraries retain their own tests. Run `task smoke` explicitly when changing the
example's integration. Real sandbox payments remain a separate manual activity.

The feed checks purchase access only for products in the selected page. When `X-Next-Cursor` is present, request the next page with `?before=<cursor>`; a page may contain fewer visible posts after private posts are filtered. Checkout return URLs are navigation only. The signed Stripe webhook establishes access.
