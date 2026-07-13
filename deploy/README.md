# Deploying session-notes (cloud mode)

A runbook for putting `session-notes server` on a public VPS: the SQLite-backed
cloud server (M3/M4), fronted by Caddy for automatic TLS, made durable with
Litestream (continuous replication) plus a nightly `VACUUM INTO` backup to a
second provider, and exported to plain markdown for format durability.

Two install routes are documented — pick one:

- **Docker Compose** (recommended): server + litestream + Caddy as containers.
- **Bare systemd**: the static binary as a service, Caddy on the host.

Contents:

1. [Provision a Hetzner VPS from zero](#1-provision-a-hetzner-vps-from-zero)
2. [Route A — Docker Compose](#2-route-a--docker-compose)
3. [Route B — bare systemd](#3-route-b--bare-systemd)
4. [Token bootstrap](#4-token-bootstrap)
5. [Litestream setup](#5-litestream-setup)
6. [Backup cron (second provider)](#6-backup-cron-second-provider)
7. [Board export (format durability)](#7-board-export-format-durability)
8. [Restore drill](#8-restore-drill)
9. [Upgrade procedure](#9-upgrade-procedure)
10. [Operational endpoints & knobs](#10-operational-endpoints--knobs)

---

## 1. Provision a Hetzner VPS from zero

1. Create a Cloud server (a CX22 / 2 vCPU / 4 GB is ample — boards are tiny).
   Pick Ubuntu 24.04. Add your SSH key during creation.
2. Point DNS: an `A` record for `notes.example.com` → the server's IPv4 (and
   `AAAA` → IPv6 if you enabled it). TLS issuance needs this resolving first.
3. First login and baseline hardening:

   ```sh
   ssh root@notes.example.com
   apt update && apt -y upgrade
   adduser --disabled-password --gecos "" deploy
   usermod -aG sudo deploy
   # Firewall: SSH + HTTP + HTTPS only.
   ufw allow OpenSSH && ufw allow 80 && ufw allow 443 && ufw --force enable
   ```

Everything below runs as `deploy` (use `sudo` where noted).

---

## 2. Route A — Docker Compose

Install Docker:

```sh
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker deploy   # re-login to pick up the group
```

Get the repo and enter the deploy dir:

```sh
git clone https://github.com/nitsanavni/session-notes.git
cd session-notes/deploy
```

Create `deploy/.env` (git-ignored — holds domain + S3 creds for litestream):

```sh
cat > .env <<'EOF'
SN_DOMAIN=notes.example.com

# Litestream → S3-compatible bucket (see section 5).
LITESTREAM_BUCKET=session-notes-backups
LITESTREAM_PATH=server.db
LITESTREAM_ENDPOINT=https://s3.eu-central-1.amazonaws.com
LITESTREAM_REGION=eu-central-1
LITESTREAM_ACCESS_KEY_ID=AKIA...
LITESTREAM_SECRET_ACCESS_KEY=...
EOF
chmod 600 .env
```

Bring it up (builds the static image from `deploy/Dockerfile`):

```sh
docker compose up -d --build
docker compose logs -f server        # watch it start
curl -fsS https://notes.example.com/healthz   # -> ok
```

Then mint the bootstrap token — [section 4](#4-token-bootstrap).

---

## 3. Route B — bare systemd

Build the static binary (on the server, or build elsewhere and `scp` it — it is
fully static, `CGO_ENABLED=0`, so it runs on any Linux):

```sh
# Go 1.26+ required.
git clone https://github.com/nitsanavni/session-notes.git
cd session-notes
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o session-notes .
sudo install -m0755 session-notes /usr/local/bin/session-notes
```

Create the service user, state dir, and install the unit:

```sh
sudo useradd --system --home /var/lib/session-notes --shell /usr/sbin/nologin session-notes
sudo install -d -o session-notes -g session-notes /var/lib/session-notes
sudo cp deploy/session-notes.service /etc/systemd/system/
```

Create the bootstrap token *before* first start ([section 4](#4-token-bootstrap)),
then:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now session-notes
systemctl status session-notes
curl -fsS http://127.0.0.1:7099/healthz    # -> ok
```

Caddy on the host for TLS:

```sh
sudo apt install -y caddy   # or the official Caddy repo
# Edit deploy/Caddyfile: set the domain and change `server:7099` to
# `127.0.0.1:7099`, then:
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
sudo SN_DOMAIN=notes.example.com systemctl restart caddy
```

Litestream as its own service — [section 5](#5-litestream-setup). Backup cron —
[section 6](#6-backup-cron-second-provider).

---

## 4. Token bootstrap

The server refuses to start with no tokens (unless `--insecure`, loopback dev
only). Mint an **admin bootstrap token** — your operator key — against the same
`--db` the server uses. It is printed once; only its SHA-256 hash is stored.

Docker Compose:

```sh
docker compose run --rm server token create --name ops
# prints the token; copy it now
```

Bare systemd (run as the service user so the file ownership matches):

```sh
sudo -u session-notes session-notes server token create \
  --name ops --db /var/lib/session-notes/server.db
```

Then, from your workstation, log in and create/grant boards:

```sh
session-notes login https://notes.example.com --token <ops-token>
session-notes remote new https://notes.example.com myboard
session-notes remote grant https://notes.example.com/b/myboard#<node> \
  --new-token scout --perm write     # prints an attach line for a sub-agent
```

Token lifecycle (audit + revoke, against the local `--db`):

```sh
session-notes server token list --db /var/lib/session-notes/server.db
session-notes server token revoke scout --db /var/lib/session-notes/server.db
```

`token list` shows name / subject / admin / created — never the secret (it is
not recoverable). Revoke is immediate: the next request bearing that token 401s.

---

## 5. Litestream setup

Litestream streams every write of `server.db` to S3-compatible storage (AWS S3,
Backblaze B2, Hetzner Object Storage, MinIO, …) with ~1s lag. Config is
`deploy/litestream.yml`; all credentials come from the env (see the `.env`
template in section 2).

**Docker Compose:** already wired — the `litestream` sidecar in
`docker-compose.yml` mounts the shared data volume and the config, and reads the
`LITESTREAM_*` env from `.env`. Verify:

```sh
docker compose logs litestream        # should show "replicating"
```

**Bare systemd:** install litestream, drop the config, run it as a service
against the live DB:

```sh
# Install the litestream binary (see litestream.io/install).
sudo cp deploy/litestream.yml /etc/litestream.yml
# Provide LITESTREAM_* via an EnvironmentFile, e.g. /etc/session-notes.env (chmod 600).
sudo systemctl enable --now litestream    # ships a unit; point it at /etc/litestream.yml
```

Litestream and the app write the same file safely: SQLite WAL mode (the server
enables it) is exactly litestream's supported mode.

---

## 6. Backup cron (second provider)

Defense in depth: litestream covers point-in-time recovery on the primary S3
bucket; `deploy/backup.sh` adds an independent nightly `sqlite3 VACUUM INTO`
snapshot pushed to a **second** provider with a retention ladder. Provider-
agnostic via `BACKUP_TOOL=restic|rclone`.

```sh
sudo install -m0755 deploy/backup.sh /opt/session-notes/backup.sh
sudo tee /etc/session-notes-backup.env >/dev/null <<'EOF'
SN_DB=/var/lib/session-notes/server.db
BACKUP_TOOL=restic
RESTIC_REPOSITORY=b2:sn-offsite:server
RESTIC_PASSWORD=<long-random>
B2_ACCOUNT_ID=...
B2_ACCOUNT_KEY=...
EOF
sudo chmod 600 /etc/session-notes-backup.env

# Nightly at 03:15 UTC.
echo '15 3 * * * root . /etc/session-notes-backup.env && /opt/session-notes/backup.sh >> /var/log/sn-backup.log 2>&1' \
  | sudo tee /etc/cron.d/session-notes-backup
```

`VACUUM INTO` is transactionally consistent while the server keeps writing, so
no downtime. The ladder keeps 7 daily / 4 weekly / 6 monthly (restic) or
daily/weekly/monthly folders (rclone).

---

## 7. Board export (format durability)

SQLite and litestream protect the *bytes*; export protects the *format*. Every
board is written as plain, re-parseable `<id>.md`, so the data outlives the
database engine and is diffable / grep-able / restorable by hand.

Local (against the DB file), wire-ready for cron:

```sh
session-notes server export --dir /var/lib/session-notes/boards \
  --db /var/lib/session-notes/server.db
```

Remote (over HTTP with your admin token — no shell on the box needed):

```sh
session-notes remote pull --all https://notes.example.com ./boards-backup
```

A daily cron that exports then commits to a private git repo gives you a full
human-readable history for free.

---

## 8. Restore drill

Practice this *before* you need it.

**From litestream (primary):**

```sh
# Stop writers first so the restore is clean.
docker compose stop server          # or: sudo systemctl stop session-notes

# Restore the latest replica into place.
litestream restore -config deploy/litestream.yml /data/server.db
#   bare: litestream restore -config /etc/litestream.yml /var/lib/session-notes/server.db

# Verify integrity + that boards are present BEFORE restarting.
sqlite3 /data/server.db "PRAGMA integrity_check;"      # -> ok
sqlite3 /data/server.db "SELECT count(*) FROM boards;" # -> expected count
session-notes server export --dir /tmp/verify --db /data/server.db
ls /tmp/verify                                          # boards render as markdown

docker compose start server         # or: sudo systemctl start session-notes
curl -fsS https://notes.example.com/healthz            # -> ok
```

**From the nightly VACUUM snapshot (second provider), if S3/litestream is lost:**

```sh
restic -r "$RESTIC_REPOSITORY" snapshots
restic -r "$RESTIC_REPOSITORY" restore latest --target /tmp/restore
gunzip -c /tmp/restore/**/server-*.db.gz > /var/lib/session-notes/server.db
sqlite3 /var/lib/session-notes/server.db "PRAGMA integrity_check;"
```

**Last resort — rebuild from markdown export:** create a fresh board per file
with `session-notes remote push <file> https://notes.example.com`.

Verification checklist after any restore: `integrity_check` = ok, board count
matches, `/healthz` = 200, one known board opens in the browser and a test edit
persists.

---

## 9. Upgrade procedure

Schema migrations are idempotent `ALTER TABLE ADD COLUMN` guards run at every
`Open`, so upgrades are forward-only and safe. Still, snapshot first.

**Docker Compose:**

```sh
cd session-notes && git pull
docker compose -f deploy/docker-compose.yml up -d --build
docker compose logs -f server
curl -fsS https://notes.example.com/healthz
```

The old container gets `SIGTERM` and drains (graceful shutdown: SSE streams end,
DB closes) within `stop_grace_period` before the new one takes over.

**Bare systemd:**

```sh
git pull
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o session-notes .
# Optional belt-and-braces snapshot:
sudo -u session-notes sqlite3 /var/lib/session-notes/server.db \
  "VACUUM INTO '/var/lib/session-notes/pre-upgrade.db'"
sudo install -m0755 session-notes /usr/local/bin/session-notes
sudo systemctl restart session-notes     # SIGTERM → graceful drain → restart
curl -fsS http://127.0.0.1:7099/healthz
```

Roll back by checking out the previous tag / reinstalling the previous binary
and restarting; the DB is backward-compatible (columns are only added).

---

## 10. Operational endpoints & knobs

- **`GET /healthz`** — unauthenticated liveness probe; `200 ok` when the process
  is serving and the DB answers. Used by uptime monitors and load balancers.
- **Request logging** — one line per request to stderr: `METHOD PATH STATUS
  DURATION`. Captured by `docker compose logs` / `journalctl -u session-notes`.
- **Body-size guard** — POST/PUT bodies over 1 MiB are refused `413` before any
  handler runs.
- **Graceful shutdown** — `SIGTERM`/`SIGINT` stops accepting, drains in-flight
  requests including SSE, then closes the DB (10s budget).
- **Flags** — `--addr host:port` (default `127.0.0.1:7099`), `--db path`,
  `--insecure` (disables auth — loopback dev only, never public).

See the repo `README.md` "Cloud" section and `session-notes docs server` for the
protocol and access model.
