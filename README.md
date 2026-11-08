# Image Harvest

Crawl a site, collect jpg/png/webp images above a minimum resolution, convert
webp to jpg, and browse everything in a web dashboard with perceptual-image
similarity.

## Build

```sh
make build        # bin/image-harvest + bin/harvest-web
make build-web    # builds the React frontend into webui/dist
make test
```

## Crawl

```sh
bin/image-harvest [-depth 2] [-min 1200x800] [-workers 4] [-maxpages 200] \
                  [-out .] [-quality 92] [-db harvest.duckdb] <domain>
```

Only high-resolution images are kept: candidates smaller than `-min`
(default **1200x800**) are rejected before download, and webp is re-encoded at
`-quality` (default **92**). Per-domain link depth is `-depth` (0 = seed page
only).

```sh
bin/image-harvest -import-domains [-list <file|url>] [-list-cache top-1m.csv] \
                  [-refresh] [-import-reset]
```

Domain discovery is a **pure list import**: the top-1M CSV is parsed and every
domain is dumped into the `domains` table. No connectivity tests, no page
content evaluation. Domains already stored are skipped, so re-importing is
idempotent; `-import-reset` wipes the table first.

The import writes in 1000-row batches, each on its own short-lived connection,
because DuckDB holds its file lock for the lifetime of a connection — that keeps
the dashboard answering while a million rows are imported (~5 min for 1M).

The older network-probing mode is still available (`-discover`, which measures
homepage image density and hi-res ratio into the same table) but is off by
default.

- BFS crawl, seed domain + subdomains; service subdomains (mail., api., …)
  are skipped and content-free subdomains are abandoned after one probe page.
- Saves to `out/YYYY-MM-DD-HH/`; records run + per-image metadata (URL,
  dimensions, size, skip reason, perceptual hash) in DuckDB.
- `-rehash` backfills perceptual hashes for images from older runs.

## Browse

```sh
bin/harvest-web -db harvest.duckdb -out .   # http://localhost:8080
```

- Runs list: domain, time, depth, pages visited, images downloaded.
- All list views are paginated server-side (`?page=&limit=`, capped at 500) and
  return `total`, so the UI can page through runs, images and the candidate
  domain list (~1M rows).
- **Metadata** page: DuckDB path/size, media root, saved/skipped image counts,
  total bytes, candidate-domain counts, last crawl/discovery, and the effective
  crawl config (depth, min resolution, quality, pages, workers).
- Run detail: image grid with metadata; click an image for a detail modal
  showing visually similar images (dHash, Hamming distance ≤ 10).

## Scrape (rate limited, high-res only)

```sh
./scripts/scrape.sh            # start/restart the scraper service
./scripts/scrape.sh --status   # progress + last log lines
./scripts/scrape.sh --tail      # follow logs/scrape.log
./scripts/scrape.sh --stop
```

`image-harvest-scrape.service` crawls every imported domain that has no
`last_crawled` mark, in rank order, and records each run in DuckDB:

| setting | default | meaning |
|---|---|---|
| `REQUEST_INTERVAL` | `1s` | minimum gap between **every** outgoing request |
| `DOMAIN_INTERVAL` | `10s` | pause between finishing one domain and the next |
| `MIN_RES` | `1200x800` | smaller images are rejected before download |
| `QUALITY` | `92` | JPEG quality for webp→jpg |
| `DEPTH` / `MAX_PAGES` | `2` / `200` | crawl breadth per domain |

Identical flags on the CLI:

```sh
bin/image-harvest -crawl-domains -interval 1s -domain-interval 10s \
                  -min 1200x800 -out /mnt/nas/Photos -db harvest.duckdb
```

## Deploy (systemd)

```sh
make deploy                 # == ./scripts/deploy.sh
DEPLOY_HOST=prod PORT=9000 ./scripts/deploy.sh
./scripts/deploy.sh --no-build     # skip the remote npm/go build
./scripts/deploy.sh --dry-run
```

The script rsyncs the source, builds the frontend + binaries **on the remote**
(cgo DuckDB, so no cross-compilation), installs a systemd *user* unit and
health-checks `/api/runs`. No sudo needed; the go toolchain is fetched
automatically through `GOPROXY` (`https://goproxy.cn,direct` by default).

| env | default | meaning |
|---|---|---|
| `DEPLOY_HOST` | `dev` | ssh host alias |
| `APP_DIR` | `apps/image-harvest` | install dir, relative to remote `$HOME` |
| `PORT` | `8088` | dashboard port |
| `MEDIA_ROOT` | `/mnt/nas/Photos` | where images are written — **must be a NAS mount** |
| `DEPTH` | `2` | crawl depth |
| `MIN_RES` | `1200x800` | minimum resolution; smaller images are skipped |
| `QUALITY` | `92` | JPEG quality for webp→jpg |
| `MAX_PAGES` | `200` | page cap per crawl |
| `WORKERS` | `4` | concurrent fetches |
| `LIST` | Umbrella top-1M zip | domain list to import |
| `IMPORT_REFRESH` | unset | `1` = re-download the list on each import |
| `IMPORT_TIMER` | `1` | install/enable the daily import timer |

`config.env` on the host is the single source of truth for these; the dashboard
shows them on the **Metadata** page. Edit the defaults in `scripts/deploy.sh`
(or override per deploy: `DEPTH=1 MIN_RES=1600x1200 ./scripts/deploy.sh`).

Remote layout:

```
~/apps/image-harvest/
  src/            synced source
  bin/            harvest-web, image-harvest
  data/           harvest.duckdb
  logs/           harvest-web.log
/mnt/nas/Photos/  harvested images (NAS, cifs/nfs)
```

### Nothing is written to the local disk

Three guards, all on by default:

1. `scripts/deploy.sh` resolves the media root with `findmnt` and aborts unless
   the filesystem type is `cifs|smb3|nfs|nfs4` (override with `NET_FS_TYPES`).
   It never creates the media root — it must already exist on the NAS.
2. The unit has `RequiresMountsFor=/mnt/nas`, `ConditionPathIsDirectory=` and an
   `ExecStartPre` that re-checks the fstype, so the service refuses to start if
   the share is unmounted.
3. `image-harvest -require-netfs` (used by `scripts/crawl.sh`) aborts a crawl
   when `-out` is not on a network filesystem, instead of silently filling the
   remote's disk.

### Crawl / discover on the remote

```sh
./scripts/crawl.sh www.nipic.com                            # uses config.env
./scripts/crawl.sh -depth 1 -min 1600x1200 example.com      # per-run override
./scripts/import-domains.sh                                  # top-1M → duckdb (no probing)
./scripts/import-domains.sh --refresh                       # re-download the list first
./scripts/import-domains.sh --reset                         # wipe table, import fresh
./scripts/domains.sh -n 200 > domains.txt                   # dump candidates
```

The import logs to `logs/import.log` and also runs daily from
`image-harvest-import.timer`. `./scripts/discover.sh` is the optional
network-probing variant.

Images are written to `$MEDIA_ROOT`, metadata to `data/harvest.duckdb`
(local disk, ~3 MB, never on the NAS). The domain list import also runs daily from `image-harvest-import.timer`.

### Ops

```sh
ssh dev 'systemctl --user status image-harvest-web'
ssh dev 'journalctl --user -u image-harvest-web -f'
ssh dev 'tail -f ~/apps/image-harvest/logs/harvest-web.log'
ssh dev 'systemctl --user restart image-harvest-web'
```

The dashboard opens DuckDB read-only **per request** and the crawler opens it
only after crawling, so a crawl and the dashboard can use the same file
(DuckDB allows one writer, so the lock window stays at milliseconds).
Enable boot-start once with `loginctl enable-linger $USER` on the remote.

## Layout

```
cmd/harvest          CLI
cmd/harvest-web      web server entry
internal/crawler     crawl + download + filtering logic
internal/harvestdb   DuckDB store (runs, images, similarity)
internal/phash       64-bit perceptual image hashing
internal/web         gin API + media routes
webui                frontend serving (embedded or from webui/dist)
web                  React + react-router frontend
deploy               systemd unit template
scripts              deploy.sh, crawl.sh
```
