# Production deployment

`insights.navidrome.org` runs from the home directory of the deploy user (UID 1000):
`docker-compose.yml` and `Caddyfile` are copied there, and that same directory is the
data volume (`.:/app`), so `reports/`, `summaries/`, and `web/chartdata/` live beside them.

Three services: `ingest` (accepts `/collect`, appends report segments and nothing else),
`process` (cron summarize/charts/purge, serves `/api/charts`), and `caddy` (TLS, routes
`/api/charts*` to `process` and everything else to `ingest`).

## Cutover checklist

Do these in order. Steps 1 and 2 must happen **before** the new compose file lands.

1. **Create `~/.env` with the API key.**

   ```bash
   printf 'API_KEY=%s\n' '<value currently inlined in ~/docker-compose.yml>' > ~/.env
   chmod 600 ~/.env
   ```

   `docker-compose.yml` uses `${API_KEY:?...}`, so `docker compose up` refuses to start
   without it rather than silently serving `/api/charts` unauthenticated. That guard is
   evaluated for the whole file, not just the `process` service: if `~/.env` is ever lost or
   emptied, **every** compose command fails, `down` and `ps` included. Recovery starts with
   recreating `~/.env` — until it exists there is no way to stop or inspect the stack with
   compose.

2. **Give UID 1000 ownership of the data directories.**

   ```bash
   mkdir -p ~/reports ~/summaries ~/web/chartdata
   chown -R 1000:1000 ~/reports ~/summaries ~/web
   ```

   `~/reports` does not exist before the first `ingest` run, so create it here: without the
   `mkdir`, `chown` fails on the missing path and this step looks like it did not work.

   Both services now run as `user: "1000:1000"`. If `~/reports` is not writable by that UID,
   `ingest` exits at startup and `restart: unless-stopped` turns it into a crash loop that
   drops every report. A `~/web/chartdata` that UID 1000 cannot write is quieter and worse:
   chart export only logs the failure, so `/api/charts` keeps serving a stale `charts.json`
   indefinitely.

3. **Copy the compose file and the Caddyfile**, from the `prod/` directory of a checkout of
   this repository on the server:

   ```bash
   cd /path/to/insights/prod && cp docker-compose.yml Caddyfile ~/
   ```

   Both files in that directory are production-ready as-is: the staging ACME block in
   `Caddyfile` is commented out. If you ever uncomment it to test issuance, re-comment it
   before deploying — staging certificates are not publicly trusted.

4. **Deploy.**

   ```bash
   cd ~ && docker compose down --remove-orphans && docker compose pull && docker compose up -d && docker compose ps
   ```

   This replaces the old single-container `insights` service with the two new ones, `ingest`
   and `process`. `--remove-orphans` is what does the replacing: the new compose file no
   longer defines `insights`, and compose does not remove a service just because it
   disappeared from the file — it reports an orphan container and leaves it running. Left
   alone, that container keeps its Go heap and SQLite connection resident on a box with no
   swap, and its own cron keeps writing `~/summaries/**` and overwriting
   `~/web/chartdata/charts.json` from an ever more stale `insights.db` — the same paths the
   new `process` writes. Expect `docker compose ps` to list exactly `caddy`, `ingest` and
   `process`; if `insights` is still there, stop and remove it before going further.

## Post-deploy checks

Both must pass. Run the first a few minutes after deploying.

1. **Reports are landing** — a segment exists for today and grows at roughly 95 lines/minute:

   ```bash
   ls -la ~/reports/$(date -u +%Y)/$(date -u +%m)/
   zcat ~/reports/$(date -u +%Y)/$(date -u +%m)/reports-$(date -u +%F).*.ndjson.gz | wc -l
   sleep 60
   zcat ~/reports/$(date -u +%Y)/$(date -u +%m)/reports-$(date -u +%F).*.ndjson.gz | wc -l
   ```

   Files are named `reports-YYYY-MM-DD.NNN.ndjson.gz`; each `ingest` restart opens a new
   segment, so expect more than one per day. Buffered data is flushed every 30s, so wait at
   least that long before comparing counts.

2. **The charts endpoint serves, and requires the key:**

   ```bash
   set -a; . ~/.env; set +a                         # compose reads ~/.env, your shell does not
   curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $API_KEY" \
     https://insights.navidrome.org/api/charts      # expect 200
   curl -s -o /dev/null -w '%{http_code}\n' \
     https://insights.navidrome.org/api/charts      # expect 401
   ```

   Sourcing `~/.env` first is not optional: without it `$API_KEY` is empty, curl sends a bare
   `Authorization: Bearer `, and the first call returns `401` no matter how healthy the stack
   is.

   A `401` on the first call, with the key actually in the environment (`echo ${#API_KEY}`
   should print a non-zero length), means the running `process` has a different key than
   `~/.env` holds — most likely it was started from another directory, since compose reads
   the `.env` next to the compose file. Compare with
   `cd ~ && docker compose exec process env | grep API_KEY` and restart it from `~` if they
   differ. A `200` on the second call means `/api/charts` is unauthenticated — stop and fix
   before leaving it running.

## After the cutover

- **Retire the rotation cron.** Remove `rotate.sh` and its crontab entry: `process` purges
  report files older than 15 days on its own, daily at 00:30 UTC.
- **Delete the old database after ~15 days.** Once that many days of report files exist and
  summaries look correct, remove `~/insights.db*` and `~/backups/`. Until then they are the
  rollback path.
