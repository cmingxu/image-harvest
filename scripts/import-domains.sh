#!/usr/bin/env bash
# Import the domain list (top-1M) into DuckDB on the deploy host.
# Pure parse + insert: no connectivity tests, no page-content evaluation.
#
#   ./scripts/import-domains.sh              # import from the cached list
#   ./scripts/import-domains.sh --refresh    # re-download the list first
#   ./scripts/import-domains.sh --upload     # push the local top-1m.csv, then import
#   ./scripts/import-domains.sh --reset      # wipe the table, then import fresh
#
# Domains already in the table are skipped, so running this repeatedly is cheap
# and idempotent. The same job runs daily via image-harvest-import.timer.
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}
FLAGS=""
RESET=""

for arg in "$@"; do
  case "$arg" in
    --refresh) FLAGS="-refresh" ;;
    --upload)  FLAGS="__UPLOAD__" ;;
    --reset)   RESET="-import-reset" ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

if [ "$FLAGS" = "__UPLOAD__" ]; then
  ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
  [ -f "$ROOT/top-1m.csv" ] || { echo "no top-1m.csv in $ROOT" >&2; exit 1; }
  echo "uploading $(du -h "$ROOT/top-1m.csv" | cut -f1) top-1m.csv → $APP_DIR/data/"
  ssh "$DEPLOY_HOST" "mkdir -p \$HOME/$APP_DIR/data"
  scp -q "$ROOT/top-1m.csv" "$DEPLOY_HOST:$APP_DIR/data/top-1m.csv"
  FLAGS=""
RESET=""
fi

ssh "$DEPLOY_HOST" "APP_DIR='$APP_DIR' FLAGS='$FLAGS' RESET='$RESET' bash -s" <<'REMOTE'
set -euo pipefail
cd "$HOME/$APP_DIR"
set -a; . ./config.env; set +a

# shellcheck disable=SC2086
exec ./bin/image-harvest -import-domains \
  -db data/harvest.duckdb \
  -list "${LIST:-https://s3-us-west-1.amazonaws.com/umbrella-static/top-1m.csv.zip}" \
  -list-cache data/top-1m.csv $RESET $FLAGS
REMOTE
