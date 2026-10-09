# Deployment

## Requirements

- Docker 24+ and Docker Compose v2
- A domain or local IP address
- 1 GB RAM minimum (2 GB recommended)

---

## Quick start (local / testing)

```bash
cp .env.example .env
# Edit .env — at minimum set JWT_SECRET to a random string
docker compose up -d
```

Server runs on `http://localhost:8080`.  
Swagger UI: `http://localhost:8080/docs`

---

## Fly.io + Neon (recommended for production)

Fully managed — no server to patch, TLS handled automatically, DB is a separate managed Postgres. Two
process groups share the same image (`fly.toml`): `app` (HTTP API, always-on) and `worker` (podcast feed
refresher — **keep this at exactly 1 machine**, never scale it, or subscribers get duplicate push
notifications for the same new episode).

### 1. Create the Neon database

- [neon.tech](https://neon.tech) → new project → region **Frankfurt (eu-central-1)** (matches
  `primary_region = "fra"` in `fly.toml` — keep app and DB in the same region to avoid cross-region
  latency on every query)
- Copy the **pooled** connection string (Neon dashboard → Connection Details → "Pooled connection") —
  looks like `postgres://user:pass@ep-xxx-pooler.eu-central-1.aws.neon.tech/tuipod?sslmode=require`.
  Use the pooled one, not the direct one — `pgxpool` already pools on our side, but Neon's pooler is
  still needed to handle the connection churn from Fly Machines starting/stopping.

### 2. Install flyctl and log in

```bash
curl -L https://fly.io/install.sh | sh
fly auth login   # opens a browser — run this yourself, not something I can do for you
```

### 3. Create the app and set secrets

`fly.toml` already exists in this repo with the app name `tuipod-api` — change the `app =` line first if
that name is taken (Fly app names are globally unique).

```bash
fly apps create tuipod-api   # or whatever you renamed it to in fly.toml

fly secrets set \
  DATABASE_URL="postgres://user:pass@ep-xxx-pooler.eu-central-1.aws.neon.tech/tuipod?sslmode=require" \
  JWT_SECRET="$(openssl rand -base64 48)" \
  WEB_URL="https://tuipod.app"   # your deployed TUIPOD-LANDING URL, not this API
```

Optional secrets (billing/push/mail — same as `.env.example`, only set what you're actually using):

```bash
fly secrets set \
  STRIPE_SECRET_KEY="sk_..." \
  STRIPE_WEBHOOK_SECRET="whsec_..." \
  STRIPE_PRICE_BASIC="price_..." \
  STRIPE_PRICE_PRO="price_..." \
  SMTP_HOST="..." SMTP_USER="..." SMTP_PASS="..." SMTP_FROM="..." \
  FIREBASE_SERVER_KEY="..." \
  METRICS_TOKEN="$(openssl rand -hex 24)"
```

### 4. Deploy

```bash
fly deploy
```

This builds the image from `Dockerfile` remotely and starts one machine per process group (`app` +
`worker`, per `fly.toml`). Migrations run automatically on `app` boot (`db.RunMigrations`), same as
local — no separate migration step.

### 5. Verify

```bash
fly status                              # both `app` and `worker` machines should show "started"
curl https://tuipod-api.fly.dev/health  # {"status":"ok"}
```

Swagger UI: `https://tuipod-api.fly.dev/docs`

### 6. Custom domain (optional)

```bash
fly certs add api.yourdomain.com
# then add the CNAME/A records Fly shows you at your DNS provider
fly secrets set WEB_URL="https://tuipod.yourdomain.com"   # the frontend, not the API
```

### Scaling later

```bash
fly scale count app=2      # more API capacity — worker stays untouched at 1
fly scale count worker=1   # never change this
```

---

## Proxmox LXC (self-hosted alternative)

An LXC container is the lightest way to run TUIPOD on Proxmox VE.

### 1. Create the container

In the Proxmox web UI:

- **Template**: Debian 12 or Ubuntu 22.04 (download via *Datacenter → local → CT Templates*)
- **CPU**: 1–2 cores
- **RAM**: 1024–2048 MB
- **Disk**: 10 GB (more if you expect large podcast libraries)
- **Network**: DHCP or a static IP on your LAN bridge (`vmbr0`)
- Enable **"Nesting"** under Options → Features (required for Docker inside LXC)

### 2. Install Docker inside the container

```bash
# Enter the container
pct enter <vmid>

# Install Docker (official script)
apt-get update && apt-get install -y curl
curl -fsSL https://get.docker.com | sh

# Test
docker run --rm hello-world
```

### 3. Deploy TUIPOD

```bash
# Clone the repo (or copy the files)
apt-get install -y git
git clone https://github.com/youruser/tuipod.git /opt/tuipod
cd /opt/tuipod

# Configure
cp .env.example .env
nano .env          # set JWT_SECRET, WEB_URL (your deployed TUIPOD-LANDING URL), and optionally Stripe keys
```

Start the stack:

```bash
docker compose -f docker-compose.yml up -d
```

Check logs:

```bash
docker compose logs -f api
```

### 4. Auto-start on reboot

```bash
# Enable Docker to start on boot
systemctl enable docker

# docker-compose.yml already has restart: unless-stopped
# so the containers restart automatically with Docker
```

### 5. Find your container's IP

```bash
ip addr show eth0 | grep "inet "
```

Point the TUI at it:

```bash
export TUIPOD_API_URL=http://<container-ip>:8080
# or set it permanently in ~/.config/tuipod/config.json
```

---

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | ✅ | — | PostgreSQL connection string |
| `JWT_SECRET` | ✅ | — | Random string ≥ 32 chars for signing JWTs |
| `PORT` | | `8080` | HTTP port |
| `WEB_URL` | | — | Public base URL of the **frontend** (TUIPOD-LANDING) — used in reset-password/verify-email emails and Stripe redirect URLs, NOT this API's own URL |
| `SMTP_HOST` | | — | SMTP server for email (forgot-password, verify-email) |
| `SMTP_PORT` | | `587` | SMTP port |
| `SMTP_USER` | | — | SMTP username |
| `SMTP_PASS` | | — | SMTP password |
| `SMTP_FROM` | | — | From address for outgoing mail |
| `STRIPE_SECRET_KEY` | | — | Stripe secret key (billing optional) |
| `STRIPE_WEBHOOK_SECRET` | | — | Stripe webhook signing secret |
| `STRIPE_PRICE_BASIC` | | — | Stripe Price ID for basic plan |
| `STRIPE_PRICE_PRO` | | — | Stripe Price ID for pro plan |
| `FIREBASE_SERVER_KEY` | | — | FCM key for push notifications (optional) |
| `METRICS_TOKEN` | | — | Bearer token to protect `/metrics` (optional) |
| `DB_MAX_CONNS` | | `4` (or CPU count if higher) | PostgreSQL connection pool max — this is `pgxpool`'s own built-in default, nothing in this codebase sets a different one unless you do. `.env.example` sets `10` for the Docker Compose path, but that file isn't used for the Fly.io deploy above — set this explicitly via `fly secrets set` if you want something other than the pgxpool default there |
| `DB_MIN_CONNS` | | `0` | PostgreSQL connection pool min (`pgxpool`'s own default) |
| `REFRESH_INTERVAL_HOURS` | | `6` | RSS background refresh interval |

Generate a secure `JWT_SECRET`:

```bash
openssl rand -base64 48
```

---

## HTTPS with Caddy (recommended reverse proxy)

Caddy handles TLS certificates automatically via Let's Encrypt.

Install Caddy in the **same** LXC container (or a separate one):

```bash
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update && apt-get install caddy
```

Create `/etc/caddy/Caddyfile`:

```
api.yourdomain.com {
    reverse_proxy localhost:8080
}
```

```bash
systemctl enable --now caddy
```

Caddy fetches and renews the certificate automatically. This only changes how the API itself is
reached — `WEB_URL` in your `.env` stays pointed at the frontend (TUIPOD-LANDING), not this
domain; no restart needed unless you're also changing `WEB_URL`.

---

## nginx alternative

If you prefer nginx, create `/etc/nginx/sites-available/tuipod`:

```nginx
server {
    listen 80;
    server_name api.yourdomain.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name api.yourdomain.com;

    ssl_certificate     /etc/letsencrypt/live/api.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.yourdomain.com/privkey.pem;

    location / {
        proxy_pass         http://127.0.0.1:8080;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
    }
}
```

Use `certbot --nginx -d api.yourdomain.com` for the certificate.

---

## Backups

The only stateful component is the PostgreSQL volume. Back it up with:

```bash
docker exec tuipod-db-1 pg_dump -U tuipod tuipod | gzip > tuipod-$(date +%Y%m%d).sql.gz
```

Restore:

```bash
gunzip -c tuipod-20260101.sql.gz | docker exec -i tuipod-db-1 psql -U tuipod tuipod
```

For automated daily backups, add a cron job on the Proxmox host or inside the LXC:

```bash
# crontab -e
0 3 * * * cd /opt/tuipod && docker exec tuipod-db-1 pg_dump -U tuipod tuipod | gzip > /opt/backups/tuipod-$(date +\%Y\%m\%d).sql.gz
```

---

## TUI — connect to your server

**Cloud mode** (login with account):

```bash
TUIPOD_API_URL=https://api.yourdomain.com TUIPOD_WEB_URL=https://tuipod.yourdomain.com ./tuipod
# or set permanently:
echo '{"api_url":"https://api.yourdomain.com","web_url":"https://tuipod.yourdomain.com"}' > ~/.config/tuipod/config.json
```

`TUIPOD_WEB_URL`/`web_url` is optional but recommended — without it the login screen's "Forgot
password?" hint stays hidden (see `TUIPOD-TUI/CLAUDE.md`). Same idea applies to the mobile app's
release builds: pass `--dart-define=WEB_URL=https://tuipod.yourdomain.com` alongside
`--dart-define=API_BASE_URL=...` (see `TUIPOD-APP/CLAUDE.md`).

**Local mode** (no server needed):

Start the TUI, press `Tab` until the **Local** tab is selected, press `Enter`.  
All data is stored in `~/.config/tuipod/local.db`.

---

## Upgrading

```bash
cd /opt/tuipod
git pull
docker compose build api worker
docker compose up -d api worker
```

Migrations run automatically on startup (in the `api` service) — no manual steps needed.

---

## Scaling the API

The `api` service is stateless and can be scaled to multiple instances (e.g. behind a load balancer —
note the current `docker-compose.yml` binds `api` to a fixed host port, so scaling with plain
`--scale` needs that removed in favor of a reverse proxy in front of the replicas first).

The `worker` service (podcast feed refresher) must **stay at exactly 1 replica**, regardless of how many
`api` instances run — it's not designed for concurrent instances, since duplicates would re-fetch the
same feeds and send duplicate push notifications.
