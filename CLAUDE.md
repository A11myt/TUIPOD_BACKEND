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

`testutil`'s `init()` also sets `MIGRATIONS_DIR` to an absolute path derived via `runtime.Caller` (fixed
2026-09-21) — needed because `go test` runs each package's test binary with that package's own source
directory as CWD, not the repo root, so `db.RunMigrations`'s default relative `"migrations"` path can't
resolve when testing anything other than the repo root itself. Don't rely on CWD for this again.

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

**Session lifetime**: access tokens live 15 minutes, refresh tokens 30 days (`GenerateAccessToken`/
`GenerateRefreshToken`, `internal/auth/jwt.go`) — but `POST /auth/refresh` always mints a **brand
new** refresh token with a fresh 30-day expiry (`newAuthResponse`), and all three clients (TUI's
`api.Client`, the App's `ApiClient._authorized`, the web player's `authFetch`) transparently call
it on any 401. Net effect: a session is a sliding 30-day window, not a fixed one — a user who opens
any client at least once a month never sees a forced re-login. Only two things end a session
early: explicit logout, and password change/reset (both revoke every refresh token for that user,
`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1`).

**Plan gating (paywall)**: `billing.RequirePlan(db, plans...)` (`internal/billing/middleware.go`) requires
the user's `plan` column to be one of the given values, else `402 Payment Required`. Applied per `r.Group`
in `main.go` — Free tier is everything that works fully offline in the client; anything requiring the
server to actually sync data (subscriptions, queue, progress, favorites, feed/history views) needs
`basic`/`pro`; `pro`-only routes (stats, OPML import) use `RequirePlan(db, "pro")` alone. There is
deliberately no subscription-count limit — Sync access itself is the paid feature. OPML export and
podcast/episode browsing stay ungated (no lock-in on your own data; catalog browsing isn't "sync").

**Admin growth stats** (2026-09-21): `GET /admin/stats/growth?days=N` (`internal/admin/handler.go`'s
`GrowthStats`, default 30/max 365 days) returns daily signup counts derived from `users.created_at` —
built for MARKETING.md's launch metrics. It's signups only, not a free→paid conversion-over-time series
— `users.plan` has no history, only the current value, so that would need a separate plan-change/event
log if ever wanted.

**Errors**: handlers respond with `internal/httperr.Write(w, status, msg)` for a consistent
`{"error": "..."}` JSON body.

**Rate limiting**: `internal/ratelimit` provides per-route in-memory limiters (`ratelimit.New(n, window)`),
applied as chi middleware in `main.go` — separate limits for global, auth, fetch, search, and import
routes, each overridable via `RATE_LIMIT_*` env vars.

**Podcast refresh**: `internal/podcast/refresher.go` runs as a background goroutine inside `cmd/worker`
(started in its `main.go`, cancelled on shutdown) that periodically re-fetches subscribed RSS feeds via
`gofeed` and notifies users of new episodes through `internal/push`. Not run inside `cmd/server` — see
the "Two binaries" note above.

Both `podcast.Fetch` (the handler — also what the TUI's manual "R"/"refresh all" hit, since they call
`POST /podcasts/fetch` again rather than `/podcasts/{id}/refresh`) and `podcast.UpsertFeed` (used by that
refresh endpoint, OPML import, and the worker's refresher) skip re-inserting episodes older than the
newest `pub_date` already stored for that podcast — a `SELECT MAX(pub_date)` up front, then an in-memory
`pubDate.Before(cutoff)` check per item, never an early `break` (feed item order isn't contractually
newest-first even though it conventionally is). Otherwise every refresh of a long-running podcast means
one `INSERT ... ON CONFLICT DO NOTHING` round trip per episode in its *entire* history, every time.
Enclosure selection takes the first enclosure regardless of its `type` attribute (some hosts leave it
blank on otherwise-valid feeds) — requiring it non-empty used to silently drop those episodes.

Both of those same code paths also call `fetchImageValidators` (`internal/podcast/rss.go`) — a
best-effort `HEAD` request (own 5s timeout, separate from the RSS fetch's) against the podcast's cover
image URL, capturing its `ETag`/`Last-Modified` into `podcasts.image_etag`/`image_last_modified`. Neither
column is ever required — a client (the TUI) uses whichever it gets to decide whether *its own* cached
copy of the image is stale, without hitting the image host itself to check; a host that sends neither
just means that client falls back to checking the image host directly. Exposed on `subscription.List`
(`SubscriptionResponse.image_etag`/`image_last_modified`, `omitempty`) — the per-podcast subs list, not
per-episode responses, since it's a podcast-level property and every episode already denormalizes
`PodcastImage` from it.

**`WEB_URL` fix (2026-09-21)**: `internal/auth/handler.go`'s reset-password/verify-email emails and
`internal/billing/handler.go`'s Stripe checkout/portal redirect URLs all used to build links off
`APP_URL`, which `.env.example` set to *this API's own* base URL (`http://localhost:8080`) — meaning
every one of those links pointed at the backend directly (e.g. a mailed reset-password link hit
`{api}/v1/auth/reset-password`, a **POST**-only JSON endpoint, so clicking it from an email could
never work at all; the verify-email link hit a real endpoint but rendered a blank `204 No Content`
page; Stripe's checkout success/cancel and billing-portal return URLs pointed at nonexistent backend
routes like `/billing/success`/`/settings`). Renamed the env var to `WEB_URL` and repointed every one
of these at frontend (TUIPOD-LANDING) pages instead: `/reset-password?token=`, `/verify-email?token=`,
`/app?checkout=success|cancel`, `/account`. See `TUIPOD-LANDING/CLAUDE.md` for the pages that now
handle these. No client (TUI/App/Web) called `POST /auth/forgot-password`, `POST
/auth/reset-password`, `PUT /me/password`, or `DELETE /me` before this fix either — Landing now has
`/account` (change password + delete account, login-only, not plan-gated) and a "Forgot password?"
flow on `/login`; TUI/App still don't expose these (tracked in the Obsidian todo).

**Security audit + fixes (2026-09-22)**: a dedicated pass looking for hardcoded secrets and
vulnerabilities across all four repos. No leaked keys/secrets found anywhere (grepped all four for
Stripe/Firebase/AWS-shaped key patterns; `.env` is gitignored and was never committed in this
repo's history). Three real, fixed findings, all backend-only:
- **SSRF (the important one)**: `POST /podcasts/fetch`, the refresh endpoint, and OPML import all
  pass a user-submitted URL straight into `gofeed.ParseURLWithContext` with zero validation — any
  authenticated user (any plan) could point it at `http://localhost:PORT`, an internal service, or
  a cloud metadata endpoint (`169.254.169.254`, readable on Fly.io/AWS/GCP) and have this server
  make that request on their behalf, with the resulting error text reflected back to them. A second
  instance of the same bug: `fetchImageValidators`'s HEAD request uses the fetched feed's own
  `<image>` URL, so a malicious feed body chains straight into the same hole. Fixed by
  `internal/podcast/safe_client.go`'s `safeHTTPClient` — a shared `http.Client` whose
  `Transport.DialContext` resolves the hostname and rejects loopback/private
  (RFC1918/RFC4193)/link-local (covers the metadata address)/unspecified/multicast IPs **at dial
  time**, then connects to the already-validated IP directly rather than letting the transport
  re-resolve the hostname — checking only the parsed URL up front would still be bypassable via DNS
  rebinding (resolve public at validation, private moments later at actual connect). Wired into all
  three call sites (`podcast.Fetch`, `podcast.UpsertFeed`, `fetchImageValidators`). The iTunes
  search proxy (`Search`) was checked too but is fine as-is — its host is a hardcoded constant, only
  the query string is user-influenced. Package tests needed `podcast.AllowPrivateNetworksForTests`
  (a `TestMain` in `handler_test.go` sets it) since they fetch from `httptest.NewServer`, which is
  always loopback — never set outside tests.
- **No server-side password length floor**: `Register`/`ResetPassword`/`ChangePassword` only ever
  checked for a non-empty password — the 8-char minimum existed solely as each frontend's
  `minLength` form attribute, trivially bypassed by calling the API directly. Now enforced
  server-side too (8-72 chars; 72 is bcrypt's own input cap — `GenerateFromPassword` silently
  truncates beyond it, so without an upper bound two different 100-char passwords sharing the same
  first 72 bytes would hash identically).
- **No request body size cap**: nothing anywhere used `http.MaxBytesReader`, so a POST/PUT to any
  route — including unauthenticated ones like `/auth/register` — could carry an arbitrarily large
  body. Fixed with one global 5 MiB cap in `main.go`'s middleware chain (generous — the largest
  legitimate body, an OPML import with thousands of subscriptions, is still well under it).

Also checked and already fine, no change needed: SQL injection (every query is parameterized, no
`fmt.Sprintf` into SQL anywhere), IDOR (every delete/update handler scopes its query by
`user_id = $N`, not just the resource ID), admin routes (properly wrapped in
`admin.Middleware(pool)`, and `is_admin` is never touched by any user-facing handler — DB-only),
Stripe webhook signature verification (`webhook.ConstructEvent` checks `Stripe-Signature`), token
randomness (`crypto/rand` for JWTs, Postgres `gen_random_uuid()` for reset/verify tokens — both
cryptographically strong), CORS (`Access-Control-Allow-Origin: *` is fine for a Bearer-token API
with no cookies — there's nothing ambient for another origin to ride along with). Rate limiting is
in-memory and per-instance (`internal/ratelimit`) — fine for the single `cmd/server` replica running
today, but won't hold once horizontally scaled; worth a shared store (Redis, or Postgres-backed) if
that becomes real.

**Optimization pass (2026-09-22)**: a performance/cost/dead-code review, separate from the security
pass above. Two real fixes, both backend-only, both verified against a populated dev DB:
- **Unbounded token-table growth (the important one)**: nothing anywhere ever deleted expired rows
  from `refresh_tokens`, `password_resets`, or `email_verifications` — and per "Session lifetime"
  above, `POST /auth/refresh` inserts a **new** `refresh_tokens` row on every single refresh (roughly
  every 15 minutes of continuous use per active session) without ever deleting the old one. Left
  running, this is unbounded growth directly on Neon-billed storage, for a project whose own
  MARKETING.md goal is getting cost-covering fast. Fixed with `internal/auth/cleanup.go`'s
  `RunTokenCleanup` — a `DELETE ... WHERE expires_at < NOW()` pass across all three tables, run once
  at startup then every 24h, wired into `cmd/worker/main.go` alongside the podcast refresher (not
  `cmd/server` — same "one process doing this on a schedule" reasoning, though the delete itself is
  idempotent and would be harmless from multiple replicas too). Revoked-but-not-yet-expired rows are
  left alone on purpose — `expires_at` is the only signal all three tables share, and there's no
  strong reason to prune those any earlier than they'd naturally expire.
- **Three redundant indexes**: `idx_subscriptions_user_id`, `idx_progress_user_id`, and
  `idx_favorites_user_id` each duplicated the leading column of that same table's
  `UNIQUE(user_id, X)` constraint index — a composite index's leading column already serves
  single-column lookups on it (leftmost-prefix rule), so these three provided zero read benefit
  while still costing extra storage and slowing every write on these (small, frequently-written)
  tables. Confirmed with `EXPLAIN` against a populated dev DB: dropping `idx_subscriptions_user_id`
  made the planner fall back to `subscriptions_user_id_podcast_id_key` for the identical query, same
  cost. Dropped in `migrations/014_drop_redundant_indexes.sql`. `idx_queue_user_id_position` is
  **not** one of these — its second column (`position`) differs from `queue`'s own
  `UNIQUE(user_id, episode_id)`, and `ORDER BY position` after `WHERE user_id = $1` genuinely needs
  it, so it stays.
- **`DEPLOYMENT.md`'s env var table had the wrong `DB_MAX_CONNS`/`DB_MIN_CONNS` defaults** (claimed
  `25`/`2`; the real default, confirmed by reading `pgxpool`'s source, is `max(4, NumCPU)`/`0` —
  nothing in this codebase sets a different value unless `DB_MAX_CONNS`/`DB_MIN_CONNS` is explicitly
  set, which the Fly.io deploy steps never do). Table corrected; `.env.example`'s `10`/`2` is a real,
  intentional value for the separate Docker Compose / Proxmox path, not a doc bug, left as-is.

**Found but deliberately not fixed** (larger scope, needs a considered decision, not "quality pass"
sized): `podcast.Refresher.refreshAll` (`internal/podcast/refresher.go`) refreshes every subscribed
podcast **sequentially**, one RSS fetch at a time, once per `REFRESH_INTERVAL_HOURS` (default 6h) —
with many subscribed podcasts this could make a refresh cycle take a long time and leaves the
256MB-`worker` machine mostly idle between fetches instead of overlapping them. A bounded worker
pool (a semaphore capping concurrent fetches, e.g. 5) would fix it, but this file has **zero existing
test coverage** (no `refresher_test.go` at all) — changing its concurrency model without a test
harness to catch regressions felt like the wrong tradeoff for this pass. Worth doing deliberately,
with tests, if refresh-cycle latency ever becomes a real complaint.

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
