# Testing

All tests are integration tests — they require a real PostgreSQL database.
Migrations run automatically before each test suite.

## Setup

### 1. Start a test database

```bash
docker run --rm -d \
  --name tuipod-test-db \
  -e POSTGRES_USER=tuipod \
  -e POSTGRES_PASSWORD=tuipod \
  -e POSTGRES_DB=tuipod_test \
  -p 5433:5432 \
  postgres:16-alpine
```

### 2. Set the environment variable

```bash
export TEST_DATABASE_URL="postgres://tuipod:tuipod@localhost:5433/tuipod_test"
```

Or add it to a `.env.test` file and source it:

```bash
echo 'TEST_DATABASE_URL=postgres://tuipod:tuipod@localhost:5433/tuipod_test' > .env.test
source .env.test
```

## Running tests

```bash
# All packages — -p 1 required, see note below
go test ./... -p 1

# Single package with verbose output
go test ./internal/auth/... -v

# With race detector (recommended before merging)
go test -race -p 1 ./...

# With coverage report
go test -p 1 -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

Tests are **skipped automatically** if `TEST_DATABASE_URL` is not set — safe to run in CI without a database.

**Why `-p 1` matters:** all packages share one `TEST_DATABASE_URL`, and `testutil.Pool` truncates
tables between tests. `go test ./...` runs different packages' test binaries in parallel by default,
which races on that shared DB (truncation mid-test, migration races) and produces flaky/wrong failures.
`-p 1` runs package test binaries one at a time, which fixes it. `make test` already includes it.

## What is tested

| Package | Tests | Coverage |
|---|---|---|
| `internal/auth` | Register (success, duplicate, missing fields), Login (success, wrong password, suspended account), Refresh (success, access token rejected), Logout (revokes token, invalid token = 204), ForgotPassword (unknown email = 204), ResetPassword (invalid token), VerifyEmail (invalid token) | Auth flow end-to-end |
| `internal/podcast` | Fetch (success, update, invalid URL), GetPodcast (not found), GetEpisodes (empty list) | RSS upsert and episode queries |
| `internal/subscription` | Subscribe (success, duplicate — no quota, removed), List (empty returns []), Unsubscribe (success) | OPML import tested via existing integration |
| `internal/favorite` | Add (success, duplicate), List (empty returns []), Remove (success) | Favorited podcasts |
| `internal/episode` | GetProgress (no progress = 0), UpsertProgress + Get, GetEpisode (not found) | Progress tracking |
| `internal/user` | Me (success), Stats (empty and with data), Feed (empty returns []) | User profile and aggregates |
| `internal/queue` | Add + List + Remove, Reorder (DB position verified), Duplicate add (409) | Queue management |
| `internal/admin` | Stats, ListUsers, GetUser (incl. 404), SetPlan (valid + invalid), Suspend + Unsuspend (DB verified), DeleteUser | Admin CRUD |
| `internal/billing` | Status (default = free), Checkout/Portal without Stripe key (501), Invalid plan (400), Webhook with bad signature (400), RequirePlan middleware (allowed + blocked/402) | Billing guard rails + plan gating |
| `internal/push` | Register (success, idempotent ON CONFLICT, invalid platform), Unregister | Device token management |

## Test helpers (`internal/testutil`)

```go
// Pool — connects, runs migrations, truncates all tables before/after each test
testutil.Pool(t) *pgxpool.Pool

// CreateUser — inserts a user with bcrypt cost 4 (fast for tests)
testutil.CreateUser(t, pool, "email@example.com", "password") string

// CreateAdminUser — same as CreateUser + sets is_admin = TRUE
testutil.CreateAdminUser(t, pool, "admin@example.com", "password") string

// CreatePodcast — inserts a minimal podcast row
testutil.CreatePodcast(t, pool) string

// CreateEpisode — inserts a minimal episode for a given podcast ID
testutil.CreateEpisode(t, pool, podcastID) string

// AccessToken — generates a valid JWT access token for a user ID
testutil.AccessToken(t, userID) string

// WithUser — injects a userID into the request context (bypasses JWT middleware)
testutil.WithUser(r, userID) *http.Request
```

## Environment variables used in tests

| Variable | Purpose |
|---|---|
| `TEST_DATABASE_URL` | PostgreSQL connection string for integration tests |
| `JWT_SECRET` | Set automatically to `test-secret-for-tuipod-tests` in `testutil.init()` |
| `STRIPE_SECRET_KEY` | Tests check that billing returns 501 when this is unset |

