#!/usr/bin/env bash
# Deploy image-harvest to a remote host over SSH.
#
#   ./scripts/deploy.sh                 # deploy to $DEPLOY_HOST (default: dev)
#   DEPLOY_HOST=prod PORT=9000 ./scripts/deploy.sh
#   MEDIA_ROOT=/mnt/nas/Photos ./scripts/deploy.sh
#   ./scripts/deploy.sh --no-build      # skip remote npm/go build
#   ./scripts/deploy.sh --dry-run       # print the plan, touch nothing
#
# Remote layout managed here:
#   $APP_DIR/src    synced source (rsync --delete)
#   $APP_DIR/bin    harvest-web + image-harvest binaries
#   $APP_DIR/data   harvest.duckdb
#   $APP_DIR/logs   service logs
#   $MEDIA_ROOT     harvested images — MUST be a network mount (NAS), never
#                   the remote's local disk. Enforced below and in the unit.
#   $APP_DIR/config.env  crawl settings shared by the web + discovery units
#
# The service is a systemd *user* unit, so no sudo is required.
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}          # relative to remote $HOME
PORT=${PORT:-8088}
ADDR=${ADDR:-:$PORT}
GOPROXY=${GOPROXY:-https://goproxy.cn,direct}
SERVICE=${SERVICE:-image-harvest-web}
MEDIA_ROOT=${MEDIA_ROOT:-/mnt/nas/Photos}       # NAS path for harvested images

# crawl settings, written to config.env and used by the units + crawl.sh
DEPTH=${DEPTH:-2}                               # link depth
MIN_RES=${MIN_RES:-600x400}                    # minimum image resolution (smaller images are skipped)
QUALITY=${QUALITY:-92}                          # JPEG quality for webp->jpg
MAX_PAGES=${MAX_PAGES:-200}
WORKERS=${WORKERS:-1}
DISCOVER_LIMIT=${DISCOVER_LIMIT:-2000}          # only used by the optional -discover probe
DISCOVER_OFFSET=${DISCOVER_OFFSET:-0}
PROBE_TIMEOUT=${PROBE_TIMEOUT:-2s}              # only used by the optional -discover probe
DISCOVER_RETRY_AFTER=${DISCOVER_RETRY_AFTER:-336h}
IMPORT_REFRESH=${IMPORT_REFRESH:-}             # set to 1 to re-download the list on import
REQUEST_INTERVAL=${REQUEST_INTERVAL:-0s}       # min delay between HTTP requests (0s = none)
DEBUG_REQUESTS=${DEBUG_REQUESTS:-1}            # 1 = log every request + image verdict
DOMAIN_INTERVAL=${DOMAIN_INTERVAL:-10s}        # delay between domains while scraping
SUBDOMAINS=${SUBDOMAINS:-0}                    # 1 = also crawl subdomains of the seed host
DOMAIN_TIMEOUT=${DOMAIN_TIMEOUT:-10m}          # abandon one domain after this long
CRAWL_OFFSET=${CRAWL_OFFSET:-}                  # one-off start position; empty = resume from the saved cursor
SCRAPE=${SCRAPE:-0}                            # 1 = start the scraper service too
LIST=${LIST:-https://s3-us-west-1.amazonaws.com/umbrella-static/top-1m.csv.zip}
TIMER=${IMPORT_TIMER:-1}                        # install the daily import timer
LIST_SYNC=${LIST_SYNC:-1}                       # upload local top-1m.csv if present
IMPORT_TIMER=${IMPORT_TIMER:-1}                 # daily domain-list import timer
NET_FS_TYPES=${NET_FS_TYPES:-cifs|smb3|nfs|nfs4}
BUILD=${BUILD:-1}
DRY_RUN=0

for arg in "$@"; do
  case "$arg" in
    --no-build) BUILD=0 ;;
    --dry-run)  DRY_RUN=1 ;;
    -h|--help)  sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m warn\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
rsh()  { if [ "$DRY_RUN" = 1 ]; then printf '  [dry-run] ssh %s %s\n' "$DEPLOY_HOST" "$*"; else ssh "$DEPLOY_HOST" "$@"; fi; }

# ---------------------------------------------------------------- preflight
log "target: $DEPLOY_HOST:$APP_DIR (port $PORT), media root $MEDIA_ROOT"

if [ "$DRY_RUN" = 0 ]; then
  ssh -o BatchMode=yes -o ConnectTimeout=8 "$DEPLOY_HOST" true \
    || die "cannot ssh to '$DEPLOY_HOST' (set DEPLOY_HOST, check ~/.ssh/config)"
fi

REMOTE_ABS=$(ssh "$DEPLOY_HOST" "echo \$HOME/$APP_DIR")
log "remote dir: $REMOTE_ABS"

# ------------------------------------------------------- NAS mount guard
# Runs before anything is created: harvested images must never land on the
# remote's local disk. We require the media root to exist and to live on a
# network filesystem (i.e. the NAS mount is actually up).
log "verifying media root is on a network mount"
MOUNT_INFO=$(ssh "$DEPLOY_HOST" "findmnt -n -o TARGET,FSTYPE -T '$MEDIA_ROOT' 2>/dev/null" || true)

if [ -z "$MOUNT_INFO" ]; then
  die "$MEDIA_ROOT does not exist on $DEPLOY_HOST — create it on the NAS (it is not created here on purpose)"
fi
MOUNT_DIR=$(printf '%s\n' "$MOUNT_INFO" | awk '{print $1}')
FS_TYPE=$(printf '%s\n' "$MOUNT_INFO" | awk '{print $2}')
printf '    %s -> fstype=%s (mountpoint %s)\n' "$MEDIA_ROOT" "$FS_TYPE" "$MOUNT_DIR"

if ! printf '%s\n' "$FS_TYPE" | grep -Eq "^($NET_FS_TYPES)$"; then
  die "$MEDIA_ROOT is on '$FS_TYPE' at '$MOUNT_DIR' — refusing: that is local disk, the NAS is not mounted.
       mount the share (e.g. 'sudo mount $MOUNT_DIR') and re-run."
fi

if [ "$DRY_RUN" = 0 ]; then
  ssh "$DEPLOY_HOST" "test -d '$MEDIA_ROOT' && test -w '$MEDIA_ROOT'" \
    || die "$MEDIA_ROOT is not writable on $DEPLOY_HOST"
  ssh "$DEPLOY_HOST" "p=\$(mktemp -p '$MEDIA_ROOT' .deploy-probe.XXXXXX) && rm -f \"\$p\"" \
    || die "$MEDIA_ROOT rejected a write probe (NAS mounted read-only or stale?)"
  printf '    write probe on NAS: ok\n'
fi

# ---------------------------------------------------------------- remote dirs
log "preparing directories"
rsh "mkdir -p '$REMOTE_ABS/bin' '$REMOTE_ABS/data' '$REMOTE_ABS/logs' '$REMOTE_ABS/src/webui'"

# ---------------------------------------------------------------- sync source
log "syncing source"
RSYNC_OPTS=(-az --delete
  --exclude .git/ --exclude bin/ --exclude .shots/
  --exclude 'web/node_modules/' --exclude 'webui/dist/'
  --exclude '*.duckdb' --exclude '*.duckdb.wal' --exclude 'top-1m.csv'
  --exclude '20*-*-*-*/' --exclude '*.log')
if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] rsync %s ./ %s:%s/src/\n' "${RSYNC_OPTS[*]}" "$DEPLOY_HOST" "$REMOTE_ABS"
else
  rsync "${RSYNC_OPTS[@]}" ./ "$DEPLOY_HOST:$REMOTE_ABS/src/"
fi

if [ "$BUILD" = 1 ]; then
  # ------------------------------------------------------------ remote build
  log "building frontend + binaries on remote (go toolchain auto-downloads if needed)"
  rsh "set -euo pipefail
    export PATH=/usr/local/go/bin:\$HOME/go/bin:\$PATH
    export GOTOOLCHAIN=auto GOFLAGS=-mod=mod CGO_ENABLED=1 GOPROXY='$GOPROXY'
    go env -w GOPROXY='$GOPROXY' >/dev/null
    cd '$REMOTE_ABS/src'
    echo '--- '\$(go version)
    (cd web && npm ci --no-audit --no-fund >/dev/null && npm run build)
    go build -trimpath -tags embed -o '$REMOTE_ABS/bin/harvest-web' ./cmd/harvest-web
    go build -trimpath -o '$REMOTE_ABS/bin/image-harvest' ./cmd/harvest
    ls -lh '$REMOTE_ABS/bin'"
else
  log "skipping remote build (--no-build)"
fi

# ---------------------------------------------------------------- unit file
# ---------------------------------------------------------------- config.env
log "writing crawl config: $REMOTE_ABS/config.env"
CONFIG_ENV=$(cat <<EOF
# managed by scripts/deploy.sh — edit defaults there, not here
PORT=$PORT
MEDIA_ROOT=$MEDIA_ROOT
DEPTH=$DEPTH
MIN_RES=$MIN_RES
QUALITY=$QUALITY
MAX_PAGES=$MAX_PAGES
WORKERS=$WORKERS
DISCOVER_LIMIT=$DISCOVER_LIMIT
DISCOVER_OFFSET=$DISCOVER_OFFSET
PROBE_TIMEOUT=$PROBE_TIMEOUT
REQUEST_INTERVAL=$REQUEST_INTERVAL
DEBUG_REQUESTS=$DEBUG_REQUESTS
DOMAIN_INTERVAL=$DOMAIN_INTERVAL
SUBDOMAINS=$SUBDOMAINS
DOMAIN_TIMEOUT=$DOMAIN_TIMEOUT
CRAWL_OFFSET=$CRAWL_OFFSET
LIST=$LIST
IMPORT_REFRESH=$IMPORT_REFRESH
EOF
)
if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] config.env: depth=%s min=%s quality=%s pages=%s workers=%s discover_limit=%s probe_timeout=%s\n' \
    "$DEPTH" "$MIN_RES" "$QUALITY" "$MAX_PAGES" "$WORKERS" "$DISCOVER_LIMIT" "$PROBE_TIMEOUT"
else
  printf '%s\n' "$CONFIG_ENV" | ssh "$DEPLOY_HOST" "cat > '$REMOTE_ABS/config.env'"
fi

# optional: push the local top-1M list so discovery doesn't re-download it
if [ "$LIST_SYNC" = 1 ] && [ -f "$ROOT/top-1m.csv" ] && [ "$DRY_RUN" = 0 ]; then
  if ssh "$DEPLOY_HOST" "test -f '$REMOTE_ABS/data/top-1m.csv'"; then
    log "top-1m.csv already on remote, skipping upload"
  else
    log "uploading local top-1m.csv ($(du -h "$ROOT/top-1m.csv" | cut -f1)) to remote data/"
    scp -q "$ROOT/top-1m.csv" "$DEPLOY_HOST:$REMOTE_ABS/data/top-1m.csv"
  fi
fi

log "installing systemd user unit: $SERVICE.service"
UNIT_LOCAL="$ROOT/deploy/$SERVICE.service"
render_unit() {
  sed -e "s|__APP_DIR__|$REMOTE_ABS|g" \
      -e "s|__ADDR__|$ADDR|g" \
      -e "s|__MOUNT_DIR__|$MOUNT_DIR|g" \
      -e "s|__OUT_DIR__|$MEDIA_ROOT|g" "$UNIT_LOCAL"
}
if [ "$DRY_RUN" = 1 ]; then
  warn "(dry-run) unit would be installed with APP_DIR=$REMOTE_ABS OUT_DIR=$MEDIA_ROOT MOUNT_DIR=$MOUNT_DIR"
  render_unit | sed -n '1,12p'
else
  render_unit > /tmp/"$SERVICE".service
  ssh "$DEPLOY_HOST" "mkdir -p ~/.config/systemd/user"
  scp -q /tmp/"$SERVICE".service "$DEPLOY_HOST:.config/systemd/user/$SERVICE.service"

  # scraper service (crawls imported domains, high-res only)
  sed -e "s|__APP_DIR__|$REMOTE_ABS|g" -e "s|__OUT_DIR__|$MEDIA_ROOT|g" -e "s|__MOUNT_DIR__|$MOUNT_DIR|g" \
    "$ROOT/deploy/image-harvest-scrape.service" > /tmp/image-harvest-scrape.service
  scp -q /tmp/image-harvest-scrape.service "$DEPLOY_HOST:.config/systemd/user/image-harvest-scrape.service"

  # domain-list import service + daily timer
  sed -e "s|__APP_DIR__|$REMOTE_ABS|g" "$ROOT/deploy/image-harvest-import.service" > /tmp/image-harvest-import.service
  scp -q /tmp/image-harvest-import.service "$DEPLOY_HOST:.config/systemd/user/image-harvest-import.service"
  scp -q "$ROOT/deploy/image-harvest-import.timer" "$DEPLOY_HOST:.config/systemd/user/image-harvest-import.timer"
fi

# ---------------------------------------------------------------- start it
log "restarting $SERVICE"
rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u)
  systemctl --user daemon-reload
  systemctl --user enable $SERVICE.service >/dev/null 2>&1 || true
  systemctl --user restart $SERVICE.service
  sleep 2
  systemctl --user --no-pager --lines=20 status $SERVICE.service || true"

# retire units from earlier revisions (probing sweep / discover timer)
rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u)
  for u in image-harvest-sweep.service image-harvest-discover.timer image-harvest-discover.service; do
    systemctl --user disable --now \$u >/dev/null 2>&1 || true
    rm -f ~/.config/systemd/user/\$u
  done
  pkill -f 'harvest -disc[o]ver' >/dev/null 2>&1 || true
  systemctl --user daemon-reload"

log "scraper service"
if [ "$SCRAPE" = 1 ]; then
  rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    systemctl --user daemon-reload
    systemctl --user enable image-harvest-scrape.service >/dev/null 2>&1 || true
    systemctl --user restart image-harvest-scrape.service
    sleep 5
    systemctl --user is-active image-harvest-scrape.service
    tail -3 '$REMOTE_ABS/logs/scrape.log' 2>/dev/null || true"
else
  rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u); systemctl --user disable --now image-harvest-scrape.service >/dev/null 2>&1 || true"
fi

log "domain-list import timer"
if [ "$IMPORT_TIMER" = 1 ]; then
  rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    systemctl --user enable --now image-harvest-import.timer
    systemctl --user list-timers image-harvest-import.timer --no-pager | head -3"
else
  rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u); systemctl --user disable --now image-harvest-import.timer || true"
fi

# linger so the unit survives logout / starts at boot (needs polkit auth once)
rsh "loginctl enable-linger \$(id -u) 2>/dev/null || echo '   note: loginctl enable-linger needs auth — run it once by hand for boot-start'"

# ---------------------------------------------------------------- health check
log "health check"
if [ "$DRY_RUN" = 0 ]; then
  ok=0
  for _ in $(seq 1 15); do
    if ssh "$DEPLOY_HOST" "curl -fsS -m 3 http://127.0.0.1:$PORT/api/runs >/dev/null"; then ok=1; break; fi
    sleep 1
  done
  [ "$ok" = 1 ] || die "health check failed — see: ssh $DEPLOY_HOST 'journalctl --user -u $SERVICE -n 50' ; ssh $DEPLOY_HOST 'tail -50 $REMOTE_ABS/logs/harvest-web.log'"
  ssh "$DEPLOY_HOST" "curl -s -o /dev/null -w '  index.html: HTTP %{http_code}\n' http://127.0.0.1:$PORT/"
  printf '  ok: api/runs responds, media root = %s\n' "$MEDIA_ROOT"
fi

log "done → http://$DEPLOY_HOST:$PORT/"
