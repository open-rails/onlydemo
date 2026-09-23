# Greenfield OpenRails fixture

This demo's focused integration lane uses a fixture boundary independent of
OpenRails' historical `internal/dbtest` and `internal/integrationharness`
packages. A test owns one disposable PostgreSQL database process scope. The
`testkit` provisioner accepts an administrative DSN from
`OPENRAILS_TEST_DATABASE_URL`, creates a unique database, returns a runtime DSN,
and drops that database on cleanup. It never imports OpenRails internals or
executes application-schema SQL.

The fixture applies OpenRails migrations only through the public
`openrails/embed.ApplyMigrations` entrypoint. It records an explicit schema
receipt by reading `pg_catalog` after migration; this is a discovery check, not
schema setup. Runtime objects are built with public `embed.New` and production
`openrails.Client` calls. HTTP assertions mount the public `embed.HTTPRoutes`
through a standard-library `ServeMux`, so embedded and remote Client paths use
the same production route boundary.

The first scenario provisions two merchants in the same disposable database.
Each merchant receives an independent embedded runtime and deterministic fake
Stripe transport with a request journal. Merchant A creates a catalog product
through `Client`; merchant B cannot retrieve it. A delegated customer token is
then read through each runtime's `/billing/v1/me/*` HTTP mount: the authenticator
maps the token to an explicit `{merchant, subject}` pair, and the foreign
merchant request is refused. No assertion reads billing tables directly.

The fake provider is closed by default and records every request, method, URL,
and body. Later scenarios can add commit-then-loss and replay cases without
changing the fixture boundary. The focused build uses a dedicated `greenfield`
Go build tag and CI command; the legacy OpenRails suite and its selectors remain
untouched while this lane accumulates receipts.
