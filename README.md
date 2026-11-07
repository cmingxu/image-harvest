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
```
