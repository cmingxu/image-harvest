#!/usr/bin/env bash
# Control image scraping on the deploy host.
#
#   ./scripts/scrape.sh                 # start/restart the scraper service
#   ./scripts/scrape.sh --status        # state, progress, last log lines
#   ./scripts/scrape.sh --stop          # stop scraping
#   ./scripts/scrape.sh --tail          # follow the scrape log
#
# The scraper (image-harvest-scrape.service) crawls every imported domain that
# has no last_crawled mark yet, in rank order:
#   * every request waits REQUEST_INTERVAL (default 1s)
#   * each domain is followed by DOMAIN_INTERVAL (default 10s)
#   * only images >= MIN_RES (default 600x400) are downloaded, saved to the NAS
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}
ACTION=start

case "${1:-}" in
  --stop)   ACTION=stop ;;
  --status) ACTION=status ;;
  --tail)   ACTION=tail ;;
  -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
  "") ;;
  *) echo "unknown flag: $1" >&2; exit 2 ;;
esac

ssh "$DEPLOY_HOST" "APP_DIR='$APP_DIR' ACTION='$ACTION' bash -s" <<'REMOTE'
set -euo pipefail
cd "$HOME/$APP_DIR"
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
SVC=image-harvest-scrape.service

case "$ACTION" in
  stop)
    systemctl --user disable --now "$SVC" 2>/dev/null || true
    systemctl --user stop "$SVC" 2>/dev/null || true
    pkill -f "crawl[-]domains" 2>/dev/null || true
    echo "scraper stopped"
    ;;
  status)
    systemctl --user status "$SVC" --no-pager --lines=0 2>&1 | head -5 || true
    echo "--- done: $(curl -s -m 5 "http://127.0.0.1:${PORT:-8088}/api/meta" | python3 -c "import json,sys; s=json.load(sys.stdin)['stats']; print(f\"{s['saved_images']} images saved, {s['runs']} runs\")" 2>/dev/null || echo 'n/a')"
    echo "--- last log:"
    tail -8 logs/scrape.log 2>/dev/null || echo "(no log yet)"
    ;;
  tail)
    tail -f logs/scrape.log
    ;;
  start)
    systemctl --user daemon-reload
    systemctl --user enable "$SVC" >/dev/null 2>&1 || true
    systemctl --user restart "$SVC"
    sleep 5
    systemctl --user is-active "$SVC"
    tail -4 logs/scrape.log 2>/dev/null || true
    ;;
esac
REMOTE
