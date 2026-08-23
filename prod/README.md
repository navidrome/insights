# Production deployment

`insights.navidrome.org` runs from the home directory of the deploy user (UID 1000):
`docker-compose.yml`, `Caddyfile` and `update.sh` are copied there, and that same
directory is the data volume (`.:/app`), so `reports/`, `summaries/` and `web/chartdata/`
live beside them.

## The environment

Three services:

- **`ingest`** accepts `/collect` and appends report segments. None of the scheduled jobs
  run here and it never reads a report back, so restarting `process` cannot interrupt
  collection.
- **`process`** runs the scheduled jobs (summarize every 2h, charts at 00:05 UTC, purge
  hourly at :30) and serves the `charts.json` it produces at `/api/charts`.
- **`caddy`** terminates TLS and routes `/api/charts*` to `process`, everything else to
  `ingest`.

Both Go services run the same image and differ by `command`, `stop_grace_period` and
environment. The image is `FROM scratch` and holds three binaries and nothing else, so
there is no shell in either container: inspect them from the host, not with
`docker compose exec`.

Reports are gzipped NDJSON under `reports/YYYY/MM/reports-YYYY-MM-DD.NNN.ndjson.gz`, one
segment per writer session. Retention follows free space rather than age: the purge
deletes whole days, oldest first, only when the volume has less than 500 MiB free, and
never touches a day younger than 7. At roughly 18 MB a day on a 9.8 GB volume that keeps
months of history. A `WARNING: ... minimum retention` line means something other than
reports is filling the disk.

Two settings are load-bearing and easy to break:

**`ingest` sets `stop_grace_period: 20s`.** On SIGTERM it gives in-flight reports 10
seconds to finish, allows up to 5 more for any handler the shutdown deadline gave up on,
and only then closes the report writer. Docker's default grace is 10 seconds, which would
SIGKILL it mid-drain and leave the open segment without its gzip trailer. Raising the
timeouts in `cmd/ingest` means raising this too.

**The ingest route sets `lb_try_duration 30s`.** Navidrome sends each report once and
treats a 502 as delivered, so without this every report arriving during a restart is
lost. Caddy holds a refused connection and retries instead. Measured against an
eight-second container swap: 75 reports lost without it, none with.

## Setting it up from scratch

Steps 1 and 2 must happen **before** the compose file lands.

1. **Create `~/.env` with the API key.**

   ```bash
   printf 'API_KEY=%s\n' "$(openssl rand -hex 24)" > ~/.env
   chmod 600 ~/.env
   ```

   `docker-compose.yml` uses `${API_KEY:?...}`, so compose refuses to start without it
   rather than silently serving `/api/charts` unauthenticated. That guard is evaluated
   for the whole file, not just the `process` service: if `~/.env` is ever lost or
   emptied, **every** compose command fails, `down` and `ps` included. Recovery starts
   with recreating `~/.env`; until it exists there is no way to stop or inspect the stack
   with compose.

2. **Give UID 1000 ownership of the data directories.**

   ```bash
   mkdir -p ~/reports ~/summaries ~/web/chartdata
   chown -R 1000:1000 ~/reports ~/summaries ~/web
   ```

   `~/reports` does not exist before the first `ingest` run, so create it here: without
   the `mkdir`, `chown` fails on the missing path and the step looks like it did nothing.

   Both services run as `user: "1000:1000"`. If `~/reports` is not writable by that UID,
   `ingest` exits at startup and `restart: unless-stopped` turns it into a crash loop that
   drops every report. A `~/web/chartdata` that UID 1000 cannot write is quieter and
   worse: chart export only logs the failure, so `/api/charts` keeps serving a stale
   `charts.json` indefinitely.

3. **Copy the three files** from the `prod/` directory of a checkout on the server:

   ```bash
   cd /path/to/insights/prod && cp docker-compose.yml Caddyfile update.sh ~/
   ```

   All three are production-ready as-is: the staging ACME block in `Caddyfile` is
   commented out. If you uncomment it to test certificate issuance, re-comment it before
   deploying, since staging certificates are not publicly trusted.

4. **Start it.**

   ```bash
   cd ~ && docker compose up -d && docker compose ps
   ```

   Expect exactly `caddy`, `ingest` and `process`.

## Checks

Both must pass. Run the first a few minutes in.

1. **Reports are landing** — a segment exists for today and grows at roughly 95
   lines/minute:

   ```bash
   ls -la ~/reports/$(date -u +%Y)/$(date -u +%m)/
   zcat ~/reports/$(date -u +%Y)/$(date -u +%m)/reports-$(date -u +%F).*.ndjson.gz 2>/dev/null | wc -l
   sleep 60
   zcat ~/reports/$(date -u +%Y)/$(date -u +%m)/reports-$(date -u +%F).*.ndjson.gz 2>/dev/null | wc -l
   ```

   Each `ingest` restart opens a new segment, so expect more than one per day. Buffered
   data is flushed every 30s, so wait at least that long before comparing counts.

   **`zcat` will warn `unexpected end of file` and exit non-zero on the newest segment.
   That is normal, not corruption** — `ingest` holds that segment open, so its final gzip
   member has no trailer until the process stops. GNU `zcat` still prints every record
   before warning, which is why the `2>/dev/null` above is enough; do not run these under
   `set -e`. The server reads the same file correctly. (BSD/macOS `gzip` is stricter and
   prints *nothing* for such a segment, so read segments on the server, not on a laptop.)

2. **The charts endpoint serves, and requires the key:**

   ```bash
   set -a; . ~/.env; set +a                         # compose reads ~/.env, your shell does not
   curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $API_KEY" \
     https://insights.navidrome.org/api/charts      # expect 200
   curl -s -o /dev/null -w '%{http_code}\n' \
     https://insights.navidrome.org/api/charts      # expect 401
   ```

   Sourcing `~/.env` first is not optional: without it `$API_KEY` is empty, curl sends a
   bare `Authorization: Bearer `, and the first call returns `401` no matter how healthy
   the stack is.

   A `401` on the first call with the key actually in the environment (`echo ${#API_KEY}`
   should print a non-zero length) means the running `process` has a different key than
   `~/.env` holds, most likely because it was started from another directory: compose
   reads the `.env` next to the compose file. Compare with

   ```bash
   docker inspect "$(docker compose ps -q process)" \
     --format '{{range .Config.Env}}{{println .}}{{end}}' | grep API_KEY
   ```

   and restart it from `~` if they differ. Read it from the host like this rather than
   with `docker compose exec`: there is no shell and no `env` in the image to exec.

   A `200` on the second call means `/api/charts` is unauthenticated. Stop and fix that
   before leaving it running.

## Routine updates

`update.sh` upgrades one service and leaves the other running:

```bash
cd ~ && ./update.sh            # process only, the usual case
cd ~ && ./update.sh ingest     # collector
cd ~ && ./update.sh both
```

Both services share an image tag, so `docker compose pull` moves it for both. A container
keeps the image it started with, so recreating only `process` leaves collection untouched.
`update.sh` prints which container was replaced and which was not, so that is visible
rather than assumed, and it health-checks each service on its own route: `/api/charts` for
`process`, `/healthz` for `ingest`.

**Never run `docker compose pull && docker compose up -d` for an update.** Compose
recreates a service whose resolved image no longer matches its running container, so after
a pull that is both of them, and `ingest` moves onto the new build as a side effect.
`update.sh` uses `up -d --no-deps <service>`. (A bare `up -d` with nothing newly pulled is
a no-op; the pull is what arms it.)

Upgrading `process` alone is safe as long as the change does not alter the on-disk report
format, since the new worker reads segments the old collector is still writing. A format
change means `both`.

The old image is kept for rollback and the script prints the command. Do not
`docker image prune` until the new one has been up for a while.

`lb_try_duration` covers a swap, not an outage: past 30 seconds Caddy gives up and reports
are lost again. `update.sh ingest` is still better run at a quiet hour than at peak.
