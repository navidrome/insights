#!/usr/bin/env bash
#
# Upgrade one service without disturbing the other.
#
# ingest and process run the same image tag and differ by command, stop_grace_period and
# environment. Because the tag is shared, one pull moves it for both. A running container
# keeps the image it started with, so recreating only the named service is what leaves
# the other alone. That is the whole trick, and it is why this script never runs a bare
# `docker compose up -d`: after a pull that recreates everything, which would carry
# ingest onto the new image as a side effect.
#
# Run it from the deploy directory, beside docker-compose.yml and .env.

set -euo pipefail

cd "$(dirname "$0")"

# Derived, not hardcoded, so it cannot drift from the compose file it is upgrading.
IMAGE="$(sed -n 's|.*image: *\(ghcr.io/navidrome/insights:[^ ]*\).*|\1|p' docker-compose.yml | head -1)"
BASE_URL="${BASE_URL:-https://insights.navidrome.org}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-60}"

usage() {
	cat <<'EOF'
Usage: ./update.sh [process|ingest|both]

  process  (default)  Upgrade the cron and charts worker.
                      Collection keeps running throughout. No reports are lost.

  ingest              Upgrade the collector.
                      Reports arriving during the swap are lost: Navidrome sends once
                      and does not retry. Prefer a quiet hour.

  both                Upgrade both, ingest last so its outage is as short as possible.

Environment:
  BASE_URL        site to health-check against (default https://insights.navidrome.org)
  HEALTH_TIMEOUT  seconds to wait for a service to answer (default 60)
  SKIP_HEALTH=1   skip the checks, for a host that cannot reach itself by name
EOF
}

case "${1:-process}" in
	process) SERVICES=(process) ;;
	ingest) SERVICES=(ingest) ;;
	both) SERVICES=(process ingest) ;;
	-h | --help | help) usage; exit 0 ;;
	*) echo "unknown service: $1" >&2; usage >&2; exit 2 ;;
esac

die() { echo "update: $*" >&2; exit 1; }

[ -f docker-compose.yml ] || die "no docker-compose.yml here. Run this from the deploy directory."
# Every compose command evaluates the API_KEY guard in the compose file, so a missing
# .env fails even `ps`, not just `up`.
[ -f .env ] || die ".env is missing. Without it every compose command fails, ps included."
[ -n "$IMAGE" ] || die "docker-compose.yml names no ghcr.io/navidrome/insights image."

# Read the key without sourcing .env. Compose's dotenv parser and bash disagree: an
# unquoted value with a space makes bash try to run the rest of the line, and under
# `set -e` that would abort mid-upgrade.
api_key="$(sed -n 's/^[[:space:]]*API_KEY=//p' .env | head -1 | sed 's/^"\(.*\)"$/\1/; s/^'\''\(.*\)'\''$/\1/')"
[ -n "$api_key" ] || die ".env has no API_KEY. Compose would refuse to start the stack."

# The image a service is running right now. Empty when it is not up: `ps -q` lists
# running containers only, so a crashed service reports nothing rather than failing.
running_image() {
	local cid
	cid="$(docker compose ps -q "$1" 2>/dev/null || true)"
	[ -n "$cid" ] || return 0
	docker inspect --format '{{.Image}}' "$cid" 2>/dev/null || true
}

# Each service answers on its own route: Caddy sends /api/charts to process and
# everything else to ingest, so checking /api/charts after an ingest swap would verify
# the wrong container.
health_url() {
	case "$1" in
		process) echo "$BASE_URL/api/charts" ;;
		ingest) echo "$BASE_URL/healthz" ;;
	esac
}

check_health() {
	local svc="$1" url code deadline auth=()
	url="$(health_url "$svc")"
	[ "$svc" = process ] && auth=(-H "Authorization: Bearer $api_key")

	deadline=$((SECONDS + HEALTH_TIMEOUT))
	while :; do
		# --max-time as well as the loop deadline: without it one hung connection
		# outlives HEALTH_TIMEOUT for as long as the OS holds the socket.
		code="$(curl -s -o /dev/null -w '%{http_code}' \
			--connect-timeout 5 --max-time 10 "${auth[@]}" "$url" 2>/dev/null)" || code=000
		[ "$code" = "200" ] && break
		[ "$SECONDS" -lt "$deadline" ] || break
		sleep 2
	done

	if [ "$code" != "200" ]; then
		echo "    $url -> $code after ${HEALTH_TIMEOUT}s"
		return 1
	fi
	echo "    $url -> 200"
}

# Roll one service back to the image it was on before this run. Each service gets its
# own line: the two can legitimately be on different images, so a single retag would put
# one of them onto the other's old build.
rollback_line() {
	local svc="$1" old="$2"
	if [ -z "$old" ]; then
		echo "  $svc was not running before this; start it with:"
		echo "    docker compose up -d --no-deps $svc"
	else
		echo "  $svc:"
		echo "    docker tag $old $IMAGE && docker compose up -d --no-deps $svc"
	fi
}

declare -A BEFORE
for svc in ingest process; do
	BEFORE[$svc]="$(running_image "$svc")"
done

echo "==> current"
docker compose ps --format 'table {{.Service}}\t{{.Status}}' 2>/dev/null || docker compose ps || true

echo "==> pulling $IMAGE"
docker compose pull "${SERVICES[0]}"
pulled="$(docker image inspect --format '{{.Id}}' "$IMAGE")"

# Decided per service, never on one service's behalf: with `both`, process being current
# must not be read as ingest being current. That mistake exits 0 having done nothing,
# which is the one failure this mode exists to prevent.
TODO=()
for svc in "${SERVICES[@]}"; do
	if [ -n "${BEFORE[$svc]}" ] && [ "${BEFORE[$svc]}" = "$pulled" ]; then
		echo "    $svc already on the newest image, skipping"
	else
		TODO+=("$svc")
	fi
done

if [ ${#TODO[@]} -eq 0 ]; then
	echo "==> nothing to do"
	exit 0
fi

failed=""
for svc in "${TODO[@]}"; do
	echo "==> recreating $svc"
	docker compose up -d --no-deps "$svc"

	if [ "${SKIP_HEALTH:-}" = "1" ]; then
		echo "    health check skipped (SKIP_HEALTH=1)"
		continue
	fi
	echo "==> checking $svc"
	if ! check_health "$svc"; then
		failed="$svc"
		break
	fi
done

echo "==> after"
docker compose ps --format 'table {{.Service}}\t{{.Status}}' 2>/dev/null || docker compose ps || true

echo "==> result"
for svc in ingest process; do
	after="$(running_image "$svc")"
	if [ -z "${BEFORE[$svc]}" ] && [ -z "$after" ]; then
		echo "    $svc: NOT RUNNING, before or after"
	elif [ -z "${BEFORE[$svc]}" ]; then
		echo "    $svc: was not running, now on ${after:0:19}"
	elif [ -z "$after" ]; then
		echo "    $svc: WAS RUNNING on ${BEFORE[$svc]:0:19}, now down"
	elif [ "${BEFORE[$svc]}" = "$after" ]; then
		echo "    $svc: untouched, still on ${after:0:19}"
	else
		echo "    $svc: replaced, ${BEFORE[$svc]:0:19} -> ${after:0:19}"
	fi
done

if [ -n "$failed" ]; then
	echo
	echo "!!! $failed did not come up. Logs:"
	echo "      docker compose logs --tail=50 $failed"
	echo "    Roll back, one line per service, in this order:"
	for svc in "${TODO[@]}"; do rollback_line "$svc" "${BEFORE[$svc]}"; done
	exit 1
fi

echo
echo "Old images kept for rollback. Do not prune until you are happy:"
for svc in "${TODO[@]}"; do rollback_line "$svc" "${BEFORE[$svc]}"; done
