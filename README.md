# OnlyDemo

A React/Fiber creator app: creators run channels and publish posts on them.
AuthKit handles people and publishing roles; OpenRails handles product offers,
purchases, recurring memberships and payment history.

The application API is `/api/v1`, identity JSON API is `/auth/v1`, and customer
billing is `/billing/v1`. The React app lives at `/`; the searchable native route
directory is `/dev/routes`. AuthKit protocol anchors stay at
`/.well-known/jwks.json` and `/oidc` when configured. The issuer stays the origin.

## Run locally

Use Go 1.26.6, Node 24, pnpm 11 and PostgreSQL 18. Copy `.env.example` to `.env`, then:

```sh
task db:up
task migrate
task run
task seed   # optional display channels and posts, created through the API
```

`task run` serves Go with Air and the frontend with Vite, both reloading on
change; open http://localhost:5173. `task run:embedded` builds the frontend into
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

Billing is required and sandbox-only. `BILLING_PSPS` lists the providers the site
offers (`stripe`, `nmi`, or `stripe,nmi`; default `stripe`); each listed provider
requires its credentials at startup and unlisted ones need none. The browser
chooses among the rails OpenRails reports for each offer.

- Stripe: **test** secret/restricted key, account ID, webhook signing secret and
  the same account's `pk_test_` publishable key. Purchases redirect to hosted
  Stripe Checkout.
- NMI: gateway ID, test-mode security key, webhook signing secret and Collect.js
  tokenization key/URL. Cards are tokenized in the page by Collect.js; the API
  only sees opaque tokens. Requires OpenRails with NMI `endpoint_deployment`
  qualification (after v0.159.0).

Public configuration carries only browser-safe values.

```sh
task stripe:listen
```

The task finds `stripe` on PATH, then `.runtime/bin/stripe`. Copy its displayed
`whsec_...` into `.env` and keep the listener running. It forwards snapshot events
to `/billing/v1/webhooks/stripe/<STRIPE_ACCOUNT_ID>`. Restricted Stripe keys need
appropriate API permissions, including Debugging Tools: Write for the listener.
Do not put executable paths or obsolete billing encryption keys in `.env`.

| Setting | Purpose |
| --- | --- |
| `DATABASE_URL` | One owning PostgreSQL connection/pool for initialization, content, AuthKit, OpenRails and River |
| `PORT`, `PUBLIC_URL` | API listener and browser return origin |
| `AUTH_ISSUER`, `AUTH_AUDIENCE` | Token issuer/audience; origin issuer matches root discovery |
| `AUTH_SCHEMA`, `APP_SCHEMA`, `BILLING_SCHEMA`, `RIVER_SCHEMA` | Optional independent schema names; shared `public` is supported |
| `BILLING_PSPS` | Enabled providers: `stripe`, `nmi` or both (default `stripe`) |
| `STRIPE_SECRET_KEY`, `STRIPE_ACCOUNT_ID`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_PUBLISHABLE_KEY` | Stripe sandbox credentials (when enabled) |
| `NMI_ACCOUNT_ID`, `NMI_SANDBOX_SECURITY_KEY`, `NMI_WEBHOOK_SIGNING_SECRET`, `NMI_TOKENIZATION_KEY`, `NMI_TOKENIZATION_URL` | NMI gateway ID, test-mode credentials and Collect.js settings (when enabled; URL optional) |

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
Channel membership currently uses a 720-hour period: **every 30 days**, not a calendar
month. Repricing moves a stable price key and preserves already accepted terms.

## Buying and membership

One-time purchases go from the frontend to the post purchase endpoint, then through the
portable OpenRails Client to hosted Stripe Checkout or an embedded NMI card form.
The host checks only content
and channel policy; OpenRails decides offer validity, repeat purchase eligibility,
accepted terms and payment state. An exact idempotency lookup happens before
mutable admission checks, so retrying an accepted attempt does not create a new
payment or lose its original terms. The UI persists the attempt key.
After a post is physically removed, its purchase endpoint returns 404; an already
accepted checkout and its financial history remain available by checkout ID.

Membership uses native customer card setup (Stripe or NMI), a saved method, an immutable
membership quote and an explicit payer confirmation. The browser uses the native
`/billing/v1/me/checkout/:id` read/confirm routes. Generic billing checkout creation
is omitted from this profile, so it cannot bypass the app's admission rules.
A redirect is never proof of payment; the UI reads the verified session state.

`/me` shows managed channels, a paginated purchased-post library, subscriptions and
billing history. Subscription cancellation uses the existing customer handlers.
No provider-owned legacy schedule is migrated by this example.

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
SMOKE_DATABASE_URL='postgres://postgres:postgres@localhost:55433/postgres?sslmode=disable' scripts/smoke.sh
```

The optional walkthrough creates and drops only its own uniquely named local
database. It runs real HTTP, AuthKit, OpenRails and River with a closed fake Stripe
transport. It covers purchases/replay, resource access, saved-card membership,
explicit quote confirmation/cancellation and retained deletion. Unexpected provider
requests fail locally; no real Stripe request or credential is used.

This demo has no automated test suite. CI builds/vets Go and builds/lints the
frontend. The libraries retain their full automated qualification. A real sandbox
purchase/subscription walkthrough is a separate deliberate activity with retained
provider receipts and idempotency keys.
