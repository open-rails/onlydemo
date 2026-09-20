# OpenRails demo

A Go/Fiber blog API with AuthKit registration, login and admin permissions, and
OpenRails billing through Stripe's test environment. Authors can sell private
posts for a one-time USD payment. OpenRails owns checkout, payment confirmation
and access grants; the app owns posts and their catalog references.

## Run locally

Install Go 1.26.6+, Docker, [Task](https://taskfile.dev) and the
[Stripe CLI](https://docs.stripe.com/stripe-cli). Then:

```sh
cp -n .env.example .env
task db:up
task migrate
task db:roles
```

Fill in the ignored `.env` with your Stripe **test** secret key and account ID.
Generate `BILLING_ENCRYPTION_KEY` once with `openssl rand -base64 32`; keep it
stable because it encrypts the stored provider credentials. Live Stripe keys
are rejected. Restricted `rk_test_` keys need permissions for the catalog,
customers, checkout, payments and webhook operations used by OpenRails.

In a separate terminal, start forwarding Stripe events:

```sh
task stripe:listen
```

Copy the displayed `whsec_...` value into `STRIPE_WEBHOOK_SECRET` in `.env`,
then run `task run`. Keep the listener running. Restricted keys additionally
need **Debugging Tools: Write** to use `stripe listen`. The webhook URL is
`/billing/v1/merchants/openrails-demo/webhooks/stripe`.

Open [localhost:3000](http://localhost:3000/) for a searchable route directory.
It reads Fiber's live route table, including native AuthKit routes and the
OpenRails webhook. There is no manually maintained endpoint list in the HTML.
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
| `DATABASE_URL` | Migration/application connection, with permission to create roles/extensions |
| `BILLING_DATABASE_URL` | Separate non-superuser, NOBYPASSRLS billing login |
| `AUTH_ISSUER`, `AUTH_AUDIENCE` | AuthKit token identity |
| `STRIPE_SECRET_KEY`, `STRIPE_ACCOUNT_ID`, `STRIPE_WEBHOOK_SECRET` | Test account and webhook credentials |
| `BILLING_ENCRYPTION_KEY` | Base64 of 32 random bytes, retained across restarts |

The default database URL is
`postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable`.
AuthKit uses `profiles`, River uses `public`, OpenRails uses `openrails`, and the
blog uses `public`. Migrations run in that order using the published embedded
sources and their migration runners (River uses its own migrator). Each
`migratekit.WithSchema(...).ApplyMigrations(...)` call creates and migrates its
target schema; the demo does not create AuthKit or OpenRails schemas directly.
The local `db:roles` task
creates the demo billing login with the password shown in `.env.example`.

Existing posts are extended by migration `002`; they are not reset. Databases
from before migratekit's numeric-ledger release need a fresh database under its
published pre-v1 contract. Point both database URLs at a new database and keep
the old one if you need its data. `task db:down` removes the entire disposable
development container; it is not a backup workflow.

AuthKit uses development signing keys and memory-backed challenge/rate-limit
storage. Email verification and MFA are disabled for this demo. Signing keys
change on restart, so sign in again after restarting the app.

## Users and admins

Register with `POST /api/v1/register`:

```json
{"identifier":"writer@example.com","username":"writer","password":"a long example password"}
```

The response includes `token_set.access_token`. Login at
`POST /api/v1/password/login` using `identifier` and `password`; that response
includes `access_token`. Send `Authorization: Bearer <access_token>` on
authenticated requests. `GET /api/v1/me` provides the current user's ID.

Grant or revoke an existing user's admin role explicitly:

```sh
task admin:grant USER_ID=<user-uuid>
task admin:revoke USER_ID=<user-uuid>
```

These operator commands use AuthKit's bootstrap API. Ordinary server startup
does not restore revoked roles. The `admin` role is an AuthKit root permission
group role granting `root:posts:read`, `root:posts:edit` and `root:posts:delete`.
The application checks those permissions through AuthKit on each request;
there is no custom admin flag/table and no trust in token-carried role names.
AuthKit's root owner also has these permissions through `root:*`.

Admins may read, edit or delete any post through the same routes as authors.
Ordinary users can edit/delete only their own posts. Writes use `RequiredLive`
and require a local user; reads also check current account status when signed
in. Bans and role revocations take effect without waiting for token expiration.

## Posts and purchases

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/api/posts` | Public posts, sale previews, and the viewer's accessible private posts |
| GET | `/api/posts/:id` | Full content if allowed; a sale preview otherwise |
| POST | `/api/posts` | Create a post owned by the authenticated user |
| PATCH | `/api/posts/:id` | Author or admin edits, including price/listing changes |
| DELETE | `/api/posts/:id` | Author or admin deletes |
| POST | `/api/posts/:id/checkout` | Start one-time Stripe Checkout; `Idempotency-Key` required |
| GET | `/api/checkouts/:id` | Read only the authenticated buyer's checkout |

Create a paid post:

```json
{
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
PATCH omission leaves the price unchanged. Ownership always comes from AuthKit.

Sale previews include title, owner, price and `can_read: false`, but omit the
body. Unlisted private posts return 404 to unauthorized viewers. Owners and
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
price, currency and buyer. Supplying those fields in the request cannot change
the charge. Checkout status is scoped to its buyer.

Access is granted only after OpenRails confirms payment through Stripe's
signed webhook pipeline. A success redirect is not proof of payment, and
unpaid, expired or forged events grant no access. Repeated delivery does not
duplicate the grant. Prices do not renew and access has no expiration; refunds
and disputes remain subject to OpenRails' billing/access policy.

This demo uses one Stripe storefront. Authors choose prices; separate seller
accounts, commissions and payouts are outside this example.

## Verify

```sh
go test ./...
go vet ./...
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable' go test -race ./...
```

Integration tests create and remove isolated databases and billing login roles,
so use an administrative test connection. They run real AuthKit and OpenRails,
real migrations and PostgreSQL, and the supported Stripe HTTP transport test
seam. Coverage includes admin grant/revoke, owner/buyer isolation, hidden bodies,
server-selected checkout terms, signed payment confirmation, replay, repricing
and delisting. CI never contacts Stripe. A real sandbox checkout additionally
requires account credentials, a running webhook listener and browser payment.
