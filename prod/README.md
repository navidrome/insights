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
   without it rather than silently serving `/api/charts` unauthenticated.

2. **Give UID 1000 ownership of the data directories.**

   ```bash
   chown -R 1000:1000 ~/reports ~/summaries ~/web
   ```

   Both services now run as `user: "1000:1000"`. If `~/reports` is not writable by that UID,
   `ingest` exits at startup and `restart: unless-stopped` turns it into a crash loop that
   drops every report. A `~/web/chartdata` that UID 1000 cannot write is quieter and worse:
   chart export only logs the failure, so `/api/charts` keeps serving a stale `charts.json`
   indefinitely.

3. **Copy the compose file and the Caddyfile.**

   ```bash
   cp docker-compose.yml Caddyfile ~/
   ```

   Both files in this directory are production-ready as-is: the staging ACME block in
   `Caddyfile` is commented out. If you ever uncomment it to test issuance, re-comment it
   before deploying — staging certificates are not publicly trusted.

4. **Deploy.**

   ```bash
   cd ~ && docker compose pull && docker compose up -d && docker compose ps
   ```

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
   curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $API_KEY" \
     https://insights.navidrome.org/api/charts      # expect 200
   curl -s -o /dev/null -w '%{http_code}\n' \
     https://insights.navidrome.org/api/charts      # expect 401
   ```

   A `401` on the first call means `~/.env` did not reach the container; a `200` on the
   second means it is unauthenticated — stop and fix before leaving it running.

## After the cutover

- **Retire the rotation cron.** Remove `rotate.sh` and its crontab entry: `process` purges
  report files older than 15 days on its own, daily at 00:30 UTC.
- **Delete the old database after ~15 days.** Once that many days of report files exist and
  summaries look correct, remove `~/insights.db*` and `~/backups/`. Until then they are the
  rollback path.
