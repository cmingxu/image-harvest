#!/usr/bin/env bash
# Probe the top-1M domain list on the deploy host and store the reachable ones
# as crawl candidates in DuckDB.
#
#   ./scripts/discover.sh                 # config.env: DISCOVER_LIMIT entries
#   ./scripts/discover.sh -discover-limit 200 -discover-offset 4000
#   ./scripts/discover.sh -refresh        # re-download the list first
#
# The same job runs daily via image-harvest-discover.timer on the host, so this
# is mainly for an immediate/ad-hoc refresh.
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}

ssh "$DEPLOY_HOST" "APP_DIR='$APP_DIR' bash -s -- $(printf '%q ' "$@")" <<'REMOTE'
set -euo pipefail
cd "$HOME/$APP_DIR"

set -a; . ./config.env; set +a

echo "discovery: limit=$DISCOVER_LIMIT offset=$DISCOVER_OFFSET timeout=$PROBE_TIMEOUT workers=$WORKERS; extra args: $*"
exec ./bin/image-harvest -discover \
  -db data/harvest.duckdb \
  -list "${LIST:-https://s3-us-west-1.amazonaws.com/umbrella-static/top-1m.csv.zip}" \
  -list-cache data/top-1m.csv \
  -discover-limit "${DISCOVER_LIMIT:-2000}" \
  -discover-offset "${DISCOVER_OFFSET:-0}" \
  -discover-timeout "${PROBE_TIMEOUT:-4s}" \
  -workers "${WORKERS:-4}" \
  "$@"
REMOTE
