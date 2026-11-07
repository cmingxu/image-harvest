#!/usr/bin/env bash
# Sweep the WHOLE top-1M list on the deploy host and keep every reachable domain
# as a crawl candidate. Probes in chunks so progress is visible and each chunk
# checks the clock: short timeouts + many workers make a 1M sweep practical.
#
#   ./scripts/sweep.sh                       # whole list, config defaults
#   SWEEP_CHUNK=50000 SWEEP_WORKERS=128 ./scripts/sweep.sh
#   ./scripts/sweep.sh -refresh              # re-download the list first
#
# Progress: ssh dev 'tail -f ~/apps/image-harvest/logs/sweep.log'
# The run is detached (setsid), so it survives your ssh session ending.
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}
SWEEP_CHUNK=${SWEEP_CHUNK:-20000}
SWEEP_WORKERS=${SWEEP_WORKERS:-96}

ssh "$DEPLOY_HOST" "APP_DIR='$APP_DIR' SWEEP_CHUNK='$SWEEP_CHUNK' SWEEP_WORKERS='$SWEEP_WORKERS' bash -s -- $(printf '%q ' "$@")" <<'REMOTE'
set -euo pipefail
cd "$HOME/$APP_DIR"
set -a; . ./config.env; set +a

cat > /tmp/sweep.sh <<'SWEEP'
#!/bin/bash
cd "$HOME/apps/image-harvest"
set -a; . ./config.env; set +a
TOTAL=$(wc -l < data/top-1m.csv)
echo "=== sweep start $(date +%F' '%T) total=$TOTAL chunk=$SWEEP_CHUNK workers=$SWEEP_WORKERS timeout=$PROBE_TIMEOUT"
off=0
while [ "$off" -lt "$TOTAL" ]; do
  echo "--- chunk offset=$off $(date +%T)"
  ./bin/image-harvest -discover -db data/harvest.duckdb -list data/top-1m.csv \
    -list-cache data/top-1m.csv \
    -discover-limit "$SWEEP_CHUNK" -discover-offset "$off" \
    -discover-timeout "${PROBE_TIMEOUT:-4s}" -workers "$SWEEP_WORKERS" | \
    grep -E "probed|reachable|chunk|stored" || true
  off=$((off + SWEEP_CHUNK))
done
echo "=== sweep done $(date +%F' '%T)"
SWEEP

chmod +x /tmp/sweep.sh
setsid nohup /tmp/sweep.sh > logs/sweep.log 2>&1 < /dev/null &
sleep 3
echo "sweep started on $HOSTNAME — log: ~/$APP_DIR/logs/sweep.log"
REMOTE
