#!/usr/bin/env bash
# Print (or save) the domain candidates stored in DuckDB on the deploy host,
# in list-rank order (the order they were imported from top-1M).
#
#   ./scripts/domains.sh                        # top 50 to stdout
#   ./scripts/domains.sh -n 500 > domains.txt   # more, to a file
#   ./scripts/domains.sh -all > domains-all.txt
#   ./scripts/domains.sh -csv > domains.csv     # domain,score,density,hi_res,samples
#
# Columns: rank  domain  score  density  hi-res%  samples  last_checked
set -euo pipefail

DEPLOY_HOST=${DEPLOY_HOST:-dev}
APP_DIR=${APP_DIR:-apps/image-harvest}
N=50
CSV=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    -n) N=${2:?}; shift 2 ;;
    -all) N=0; shift ;;
    -csv) CSV=1; shift ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

ssh "$DEPLOY_HOST" "python3 - $N $CSV" <<'REMOTE'
import json, sys, urllib.request

n, csv = int(sys.argv[1]), sys.argv[2] == "1"

# the API is paginated: walk it in 500-row pages (0 = fetch everything)
rows, page = [], 1
while True:
    url = f"http://127.0.0.1:8088/api/domains?limit=500&page={page}&sort=id"
    with urllib.request.urlopen(url, timeout=60) as r:
        payload = json.load(r)
    chunk = [d for d in payload["domains"] if d["domain"]]
    rows.extend(chunk)
    total = payload.get("total", len(rows))
    if not chunk or len(rows) >= total or (n and len(rows) >= n):
        break
    page += 1

if n:
    rows = rows[:n]

if csv:
    print("domain,score,density,hi_res_ratio,samples,last_checked")
    for d in rows:
        print(f'{d["domain"]},{d["score"]:.1f},{d["img_density"]},{d["hi_res_ratio"]:.2f},{d["samples"]},{d["last_checked"]}')
else:
    for i, d in enumerate(rows, 1):
        print(f'{i:4d}  {d["domain"]:<40} score={d["score"]:6.1f} density={d["img_density"]:<3} '
              f'hi-res={d["hi_res_ratio"] * 100:3.0f}% samples={d["samples"]} {d["last_checked"]}')
    print(f"\n{len(rows)} of {total} candidates", file=sys.stderr)
REMOTE
