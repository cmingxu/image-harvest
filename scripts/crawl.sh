#!/usr/bin/env bash
# Run a crawl on the deploy host with the deployed paths and config.
#
#   ./scripts/crawl.sh www.nipic.com                      # uses config.env
#   ./scripts/crawl.sh -depth 0 -maxpages 5 example.com   # override per run
#   ./scripts/crawl.sh -min 1600x1200 -quality 95 example.com
#
# Crawl depth, minimum resolution, JPEG quality and page cap come from
# $APP_DIR/config.env (written by scripts/deploy.sh) unless overridden.
# Images land in $MEDIA_ROOT on the NAS: -require-netfs aborts the crawl if the
# share is not mounted, so nothing is ever written to the remote's local disk.
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}

[ "$#" -ge 1 ] || { sed -n '2,12p' "$0"; exit 2; }

ssh "$DEPLOY_HOST" "APP_DIR='$APP_DIR' bash -s -- $(printf '%q ' "$@")" <<'REMOTE'
set -euo pipefail
cd "$HOME/$APP_DIR"

# crawl settings + media root from the deployed config
set -a; . ./config.env; set +a

fs=$(findmnt -n -o FSTYPE -T "$MEDIA_ROOT") || { echo 'NAS not mounted' >&2; exit 1; }
case "$fs" in
  cifs|smb3|nfs|nfs4) ;;
  *) echo "refusing: $MEDIA_ROOT is on local fs ($fs)" >&2; exit 1 ;;
esac

echo "config: depth=$DEPTH min=$MIN_RES quality=$QUALITY max_pages=$MAX_PAGES workers=$WORKERS"
exec ./bin/image-harvest -require-netfs \
  -depth "${DEPTH:-2}" -min "${MIN_RES:-1200x800}" -quality "${QUALITY:-92}" \
  -maxpages "${MAX_PAGES:-200}" -workers "${WORKERS:-4}" \
  -out "$MEDIA_ROOT" -db data/harvest.duckdb "$@"
REMOTE
