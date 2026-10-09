# TUIPOD_BACKEND

Go API server for TUIPOD (auth, podcasts/episodes, subscriptions, queue, billing, push, admin). See
`CLAUDE.md` for architecture, `TESTING.md` for the automated test suite, `DEPLOYMENT.md` for production
(Fly.io + Neon).

## Local development

This is the fast native-process loop for actively developing the backend — a `go run` process against a
plain Postgres container, restarting instantly on every code change. (For running the whole stack
Docker-built, closer to production, see `DEPLOYMENT.md`'s "Quick start" instead — slower to iterate on
since it rebuilds the image each time.)

### 1. Start a persistent dev database

```bash
docker volume create tuipod-dev-db-data
docker run -d --name tuipod-dev-db --restart unless-stopped \
  -e POSTGRES_USER=tuipod -e POSTGRES_PASSWORD=tuipod -e POSTGRES_DB=tuipod \
  -v tuipod-dev-db-data:/var/lib/postgresql/data \
  -p 5434:5432 postgres:16-alpine
```

Port `5434`, not `5432` — deliberately different from both the OS default (avoids clashing with any other
local Postgres) and `5433` (the throwaway `TEST_DATABASE_URL` container from `TESTING.md`, which is a
*separate* database that gets truncated between test runs — never point `.env` at that one). Named volume
+ no `--rm`, so your test subscriptions/episodes survive `docker stop`/`docker start` and even container
recreation.

Stop it anytime with `docker stop tuipod-dev-db`; bring it back with `docker start tuipod-dev-db` (data
persists in the volume either way). To wipe it and start over: `docker rm -f tuipod-dev-db && docker
volume rm tuipod-dev-db-data`, then repeat the `docker run` above.

### 2. Configure `.env`

```bash
cp .env.example .env
```

Edit `.env`:

- `DATABASE_URL=postgres://tuipod:tuipod@localhost:5434/tuipod`
- `JWT_SECRET=` — any random string (`openssl rand -hex 32`)
- `PORT=8081` (or whatever's free on your machine — `8080` is a common default other local services grab
  too; check with `ss -ltnp | grep :8080` or similar before assuming it's open)
- Everything else (Stripe, SMTP, Firebase, metrics token) can stay blank — those integrations degrade
  gracefully to stdout logging / a `501` when their env var is unset (see `CLAUDE.md`), which is exactly
  what you want for local dev.

### 3. Run it

```bash
make run           # go run ./cmd/server — the API, applies migrations automatically on startup
make run-worker     # optional, separate terminal — only needed to test podcast auto-refresh / push
```

Migrations apply automatically against whatever `DATABASE_URL` points at — no separate migrate step.
Swagger UI at `http://localhost:$PORT/docs`, raw spec at `/openapi.yaml`.

### 4. Point a client at it

- **TUI**: `TUIPOD_API_URL=http://localhost:8081 tuipod` (or `make run` in `TUIPOD-TUI/`), then pick
  "Cloud" at the login screen and register a (fake, local-only) account.
- **Landing/web player**: set its API base URL env var to the same `http://localhost:8081` (see
  `TUIPOD-LANDING/CLAUDE.md`).

### Everything fully local, no backend at all

The TUI also has a "Local" mode (SQLite, no server) — for anything that doesn't specifically need to
exercise the backend (most UI/UX work), that's faster to set up than any of the above. See
`TUIPOD-TUI/README.md`.
