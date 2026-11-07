#!/usr/bin/env bash
# Run a crawl on the deploy host with the deployed paths.
#
#   ./scripts/crawl.sh www.nipic.com
#   ./scripts/crawl.sh -depth 2 -maxpages 200 -min 800x600 example.com
#   DEPLOY_HOST=prod ./scripts/crawl.sh example.com
#
# Images land in $MEDIA_ROOT on the NAS (never on the remote's local disk: the
# CLI is invoked with -require-netfs, which aborts if the mount is missing).
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}
MEDIA_ROOT=${MEDIA_ROOT:-/mnt/nas/Photos}

[ "$#" -ge 1 ] || { sed -n '2,10p' "$0"; exit 2; }
ARGS=$(printf '%q ' "$@")

ssh "$DEPLOY_HOST" "set -euo pipefail
  cd \$HOME/$APP_DIR
  fs=\$(findmnt -n -o FSTYPE -T '$MEDIA_ROOT') || { echo 'NAS not mounted' >&2; exit 1; }
  case \"\$fs\" in cifs|smb3|nfs|nfs4) ;; *) echo \"refusing: $MEDIA_ROOT is on local fs (\$fs)\" >&2; exit 1;; esac
  exec ./bin/image-harvest -require-netfs -out '$MEDIA_ROOT' -db data/harvest.duckdb $ARGS"
