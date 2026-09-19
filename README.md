# OpenRails demo

A local Go/Fiber blog API using AuthKit for registration, login and sessions.
PostgreSQL 18 stores AuthKit users in `profiles` and blog posts in `public`.
Billing integration will follow later.

## Run

Install Go 1.26.6+, Docker and [Task](https://taskfile.dev), then run:

```sh
task db:up
task migrate
task run
```

Postgres uses port `55433`; the API uses port `3000`. If Postgres is still
initializing, wait until its logs show it is ready before migrating.
Startup also applies the embedded AuthKit and application migrations.

`task db:down` removes the disposable development container. It is not a
backup/persistent-storage workflow.

Configuration is read through Koanf from `PORT`, `DATABASE_URL`, `AUTH_ISSUER`
and `AUTH_AUDIENCE`. The local database URL is:

```text
postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable
```

AuthKit uses development signing keys and in-process challenge/rate-limit
storage. Email verification and MFA are disabled for this local demo. Signing
keys change on restart, invalidating old access tokens.

## Authentication and posts

Register with `POST /api/v1/register`:

```json
{"identifier":"writer@example.com","username":"writer","password":"a long example password"}
```

The response includes `token_set.access_token`. Login with
`POST /api/v1/password/login` and the same `identifier` and `password`; its
response includes `access_token`. Send `Authorization: Bearer <access_token>`
on authenticated requests.

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/api/posts` | Public posts plus the current user's own posts |
| GET | `/api/posts/:id` | Public post or the current user's own private post |
| POST | `/api/posts` | Create a post owned by the current user |
| PATCH | `/api/posts/:id` | Update only an owned post |
| DELETE | `/api/posts/:id` | Delete only an owned post |

Create accepts `slug`, `title`, `body` and optional `visibility` (`public` or
`private`, default `private`). PATCH accepts any of those fields. Ownership is
always taken from verified AuthKit claims. Hidden or non-owned posts return
404 on requests that cannot access them.

Routes use `authkitfiber.Optional`/`Required`; handlers read
`authkitfiber.UserClaims(c)`. Optional authentication rejects a supplied invalid
token. User-only writes also check the accessor's boolean because Required
accepts other AuthKit principal types. AuthKit's own routes use its canonical
mount through `authkitfiber.Fallback`.

## Verify

```sh
go test ./...
go vet ./...
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable' go test -race ./...
```

Integration tests create and remove their own database on the specified local
Postgres server; the database role therefore needs `CREATEDB` permission.
