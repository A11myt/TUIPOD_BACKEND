# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

TUIPOD-BACKEND is the Go API server for TUIPOD, a podcast app. It's a REST API (chi router) backed by
PostgreSQL, serving a companion TUI client (not in this repo). Handles auth, podcast/episode search via
RSS, subscriptions, listening progress/queue, Stripe billing, push notifications, and an admin panel.

## Commands

```bash
make run             # go run ./cmd/server (HTTP API)
make run-worker       # go run ./cmd/worker (podcast feed refresher — run only 1 instance)
make build            # builds bin/tuipod-api
make build-worker      # builds bin/tuipod-worker
make test             # go test ./... -count=1 -race -p 1
make lint             # go vet ./...
make deploy           # docker compose build api && docker compose up -d

# Single package / single test
go test ./internal/auth/... -v
go test ./internal/auth/... -run TestLogin -v

# Coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Running tests locally

All tests are **integration tests requiring a real Postgres instance** — there are no mocks. They are
automatically skipped if `TEST_DATABASE_URL` is unset (safe for CI without a DB configured).

```bash
docker run --rm -d --name tuipod-test-db \
  -e POSTGRES_USER=tuipod -e POSTGRES_PASSWORD=tuipod -e POSTGRES_DB=tuipod_test \
  -p 5433:5432 postgres:16-alpine

export TEST_DATABASE_URL="postgres://tuipod:tuipod@localhost:5433/tuipod_test"
go test ./... -p 1
```

`-p 1` is required: all packages share one `TEST_DATABASE_URL`, and `testutil.Pool` truncates tables
between tests — running package test binaries in parallel (Go's default) races on that shared DB.

See TESTING.md for the full per-package test matrix.

## Architecture

**Layout**: `cmd/server/main.go` wires everything together — it's the only place that constructs
handlers, mounts routes, and applies middleware. Each `internal/<domain>` package (auth, user, podcast,
episode, subscription, queue, billing, admin, push) is self-contained: a `handler.go` exposing
`NewHandler(pool *pgxpool.Pool) *Handler` with methods used directly as `http.HandlerFunc`s, plus a
`handler_test.go` covering it. There is no service/repository layering — handlers talk to `pgxpool.Pool`
directly with raw SQL (no ORM).

**Two binaries**: `cmd/server` runs the HTTP API only and owns migrations (`db.RunMigrations` on startup);
`cmd/worker` runs only the podcast feed refresher (`internal/podcast/refresher.go`) as a standalone
process, assuming the schema is already migrated. This split exists so the API can be scaled to multiple
instances (horizontal scaling) without the refresher running once per instance, which would duplicate RSS
fetches and send duplicate push notifications to subscribers. Always run exactly **one** `cmd/worker`
replica, no matter how many `cmd/server` instances are running. `fly.toml` maps these to Fly process
groups `app` and `worker` — see `DEPLOYMENT.md` for the Fly.io + Neon production deploy.

**Migrations**: plain numbered SQL files in `migrations/` (`00N_name.sql`), applied in order at startup
by `internal/db.RunMigrations` (tracked in a `schema_migrations` table). No down-migrations. Add a new
file with the next number to change schema — never edit an already-applied migration.

**Auth**: JWT access + refresh tokens (`internal/auth/jwt.go`). `auth.Middleware` reads the `Authorization:
Bearer` header, validates the token is `TokenType == "access"`, and stashes the user ID in request context
(`auth.UserIDFromCtx(r)`). `internal/admin/middleware.go` layers on top, requiring `is_admin = true` in
the DB. Route protection is composed in `main.go` via `r.Group` blocks, not per-handler checks.

**Plan gating (paywall)**: `billing.RequirePlan(db, plans...)` (`internal/billing/middleware.go`) requires
the user's `plan` column to be one of the given values, else `402 Payment Required`. Applied per `r.Group`
in `main.go` — Free tier is everything that works fully offline in the client; anything requiring the
server to actually sync data (subscriptions, queue, progress, favorites, feed/history views) needs
`basic`/`pro`; `pro`-only routes (stats, OPML import) use `RequirePlan(db, "pro")` alone. There is
deliberately no subscription-count limit — Sync access itself is the paid feature. OPML export and
podcast/episode browsing stay ungated (no lock-in on your own data; catalog browsing isn't "sync").

**Errors**: handlers respond with `internal/httperr.Write(w, status, msg)` for a consistent
`{"error": "..."}` JSON body.

**Rate limiting**: `internal/ratelimit` provides per-route in-memory limiters (`ratelimit.New(n, window)`),
applied as chi middleware in `main.go` — separate limits for global, auth, fetch, search, and import
routes, each overridable via `RATE_LIMIT_*` env vars.

**Podcast refresh**: `internal/podcast/refresher.go` runs as a background goroutine inside `cmd/worker`
(started in its `main.go`, cancelled on shutdown) that periodically re-fetches subscribed RSS feeds via
`gofeed` and notifies users of new episodes through `internal/push`. Not run inside `cmd/server` — see
the "Two binaries" note above.

**Optional integrations degrade gracefully, not via feature flags**: billing (`internal/billing`) returns
`501` if `STRIPE_SECRET_KEY` is unset; push (`internal/push/fcm.go`) and mailer (`internal/mailer`) log to
stdout instead of sending if their respective keys/SMTP config are unset. Check env vars directly at the
call site rather than adding a config abstraction.

**Testing conventions**: `internal/testutil` provides `Pool(t)` (connects, migrates, truncates between
tests), `CreateUser`/`CreateAdminUser`/`CreatePodcast`/`CreateEpisode` fixtures, `AccessToken(t, userID)`,
and `WithUser(r, userID)` to inject an authenticated context without going through the JWT middleware.
Every handler test uses these instead of hitting real HTTP auth or mocking the DB.

**Docs**: the top-level `docs/` package embeds `docs/openapi.yaml` and serves it at `/openapi.yaml`, plus
a Swagger UI at `/docs`. Update the OpenAPI spec when routes change.

**Observability**: `internal/metrics` registers Prometheus DB pool stats and wraps all routes via
`metrics.Middleware`; exposed at `/metrics`, optionally gated by `METRICS_TOKEN`.

## Conventions

**Function comments**: every new function must have a short doc comment (standard Go
`// FuncName ...` style) describing what it does before it's committed.

**Timestamps in response structs must be `time.Time`/`*time.Time`, never `string`/`*string`**: `pgx` v5
cannot scan a `timestamptz` column into a `string`-typed destination (with or without a pointer) — it
fails at scan time, which is easy to miss because handlers generally `continue` past per-row scan errors
in list endpoints (silently drops rows, looks like "just empty") or return a generic 404/500 for
single-row queries. `time.Time` marshals to RFC3339 in JSON automatically, which is the correct API
output anyway. This bit multiple handlers before being caught — grep for `Scan(` in a new handler and
double check every `_at`/`pub_date`-style column lands in a `time.Time` field.
