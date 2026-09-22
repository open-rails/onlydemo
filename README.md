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
`/billing/v1/merchants/openrails-demo/webhooks/stripe/<STRIPE_ACCOUNT_ID>`.

Open [localhost:3000](http://localhost:3000/) for a searchable route directory.
It reads Fiber's live route table, including the configured native AuthKit and OpenRails
route bundles. There is no manually maintained endpoint list in the HTML.
The homepage is an API directory; use the API examples below to create posts
and open the returned Stripe Checkout URL to pay with a
[Stripe test card](https://docs.stripe.com/testing).

Without `STRIPE_SECRET_KEY`, registration, ordinary posts and moderation still
work; selling and checkout return 503. Partial billing configuration fails at
startup. `GET /health` checks the database and configured billing runtime.

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
`go run .` and `go run . serve` start the server; `go run . help` prints usage.
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
webhook handler or handwritten callback registrations. AuthKit's identity routes
mount at their configured root anchors; this demo mounts billing under `/billing`.
Remote OpenRails consumers only need its remote client, with no local runtime or
route bundle. AuthKit's client boundary permits a future remote transport; this
demo uses its supported embedded runtime.

This demo chooses one host-owned River fleet for both libraries. `jobs.go`
initializes the host's River schema during migration. After both services and
merchant configuration are ready, `riverkit.New` composes `auth.runtime.RiverJobs()` and
`billing.runtime.RiverJobs()` into one unstarted client and binds producers automatically.
Without billing, the same composer receives only AuthKit's contribution. The host starts and stops workers before closing
library services, then closes its pool. OpenRails and River borrow that pool.
AuthKit creates and owns a schema-bound pool from the same connection settings;
closing it leaves the host pool open. A `MaxConns=1` host pool therefore does not
limit the whole process to one database connection.
Every replica must register the same complete schedules, since River's
elected leader schedules periodic jobs. A consumer that wants a library-managed
fleet can omit the host integration and let the library initialize its queue.

The local container provides the default `postgres` login. A normal PostgreSQL
login that owns its database also works; integration tests use that setup. There
are no per-library logins or permission-group roles. Merchant authorization uses
explicit scoped queries, independently of database-role flags.

The demo keeps one initial application migration. When upgrading from an older
version of that schema, point `DATABASE_URL` at a new database to preserve the old
one. Startup never rewrites historical checksums or drops databases; libraries
apply their own forward migrations during normal upgrades.
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

Register with `POST /api/v1/register`:

```json
{"identifier":"writer@example.com","username":"writer","password":"a long example password"}
```

The response includes `token_set.access_token`. Login at
`POST /api/v1/password/login` using `identifier` and `password`; that response
includes `access_token`. Send `Authorization: Bearer <access_token>` on
authenticated requests. `GET /api/v1/me` provides the current user's ID.

`DELETE /api/v1/user` deletes the account after fresh authentication (or a
`password` in the request body). First transfer or delete every channel it owns.
During the 30-day recovery period, a correct password login returns 409 with
`error.metadata.recovery.token` instead of a session. Explicitly confirm with
`POST /api/v1/account/recovery/confirm` and `{"token":"<recovery-token>"}`, then
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

Create a channel with `POST /api/channels` and `{"slug":"our-channel","name":"Our channel"}`.
The authenticated creator becomes its owner. `GET /api/channels/:id` reads it;
AuthKit's native `/api/v1/channel/:slug` routes manage members, roles, invitations
and settings. Native channel creation/deletion is disabled so it cannot bypass
the app's coordinated lifecycle. A channel must retain a valid owner; transfer
ownership or delete it before deleting its owner's account.
The demo's content/channel routes require native user access tokens. AuthKit's
group API also supports remote-application owners managing or transferring roles.

`DELETE /api/channels/:id` returns 202 after atomically marking deletion and
enqueuing a River job. That job archives catalog products, removes posts, and
removes the AuthKit group and application metadata. Retries are durable; payment
history and purchased-access records remain in OpenRails. No automatic refund
or provider subscription cancellation is performed.

Channel writes use `client.ForCatalogOwner(channelID)` after a live AuthKit
permission check. Root moderation uses the merchant client while preserving the
channel catalog. Creator payouts and Stripe Connect are separate features.

Catalog/provider work runs outside blog database transactions so a shared pool
cannot deadlock waiting for itself. Post updates use an atomic revision check;
concurrent edits can return 409 and should be retried after fetching the post.
Checkout captures the selected immutable offer, so a later price change does not
change an already-created checkout's terms. The billing product label keeps the
title from its first listing. Later title edits change the blog post only, so a
rejected concurrent edit cannot leave its title in billing.

| Method | Route | Behavior |
| --- | --- | --- |
| POST | `/api/channels` | Create a channel and its AuthKit permission group |
| GET | `/api/channels/:id` | Read an accessible channel |
| DELETE | `/api/channels/:id` | Authorize through channel settings permission and queue durable cleanup |
| GET | `/api/posts` | One page of public posts, sale previews, and accessible private posts (`limit=1..100`, default 50) |
| GET | `/api/posts/:id` | Full content if allowed; a sale preview otherwise |
| POST | `/api/posts` | Create a post in an authorized channel; record the author |
| PATCH | `/api/posts/:id` | Channel owner/editor or root moderator edits |
| DELETE | `/api/posts/:id` | Channel owner/editor or root moderator deletes |
| POST | `/api/posts/:id/checkout` | Start one-time Stripe Checkout; `Idempotency-Key` required |
| GET | `/api/checkouts/:id` | Read only the authenticated buyer's checkout |

OpenRails also exposes its native customer billing group under `/billing/v1/me`.
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
curl -X POST http://localhost:3000/api/posts/1/checkout \
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

## Verify

```sh
go test ./...
go vet ./...
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable' go test -race ./...
```

Integration tests create and remove isolated databases and normal database-owner
logins, so use an administrative test connection. All application/library
initialization and runtime then use the same owner pool without role memberships.
The full purchase journey runs with default schemas and `MaxConns=1`, and again
with custom billing/River schemas and all four components sharing `public`.
Shared-schema cases also test initializer order, concurrent/repeated initialization,
managed River migrations, and CLI migration/admin commands. Tests verify both libraries' scheduled jobs,
borrowed-pool shutdown ownership, distinct author catalogs, moderator pricing,
and zero persisted provider-secret rows without an encryption key. A concurrent
edit regression borrows the same pool during billing handoff and checks the 409
revision conflict preserves the committed content.
They run real AuthKit, OpenRails, migrations and PostgreSQL with the supported
Stripe HTTP transport seam. Coverage also includes owner/buyer isolation, hidden
bodies, server-selected checkout terms, signed payment confirmation, replay,
repricing and delisting. CI never contacts Stripe. A real sandbox checkout additionally
requires account credentials, a running webhook listener and browser payment.

The feed checks purchase access only for products in the selected page. When `X-Next-Cursor` is present, request the next page with `?before=<cursor>`; a page may contain fewer visible posts after private posts are filtered. Checkout return URLs are navigation only. The signed Stripe webhook establishes access.
