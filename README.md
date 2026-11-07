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
bin/image-harvest [-depth 2] [-min 800x600] [-workers 4] [-maxpages 500] \
                  [-out .] [-quality 85] [-db harvest.duckdb] <domain>
```

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
- Run detail: image grid with metadata; click an image for a detail modal
  showing visually similar images (dHash, Hamming distance ≤ 10).

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

### Crawl on the remote

```sh
./scripts/crawl.sh -depth 2 -maxpages 200 -min 800x600 www.nipic.com
```

Images are written to `$MEDIA_ROOT`, metadata to `data/harvest.duckdb`.

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
