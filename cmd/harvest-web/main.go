// harvest-web serves the crawl dashboard: run metadata, image metadata with
// perceptual similarity, and the harvested image files.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"image-harvest/internal/harvestdb"
	"image-harvest/internal/web"
	"image-harvest/webui"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "harvest.duckdb", "duckdb metadata file")
	out := flag.String("out", ".", "harvest output directory (media root)")
	flag.Parse()

	// The dashboard only reads. Each request opens its own short-lived read-only
	// connection so a crawl run can take the single DuckDB write lock in between.
	open := func() (*harvestdb.Store, func(), error) {
		// a crawl or list import may hold DuckDB's write lock for a moment;
		// retry briefly instead of failing the request
		var err error
		for attempt := 0; attempt < 40; attempt++ {
			var store *harvestdb.Store
			if store, err = harvestdb.OpenReadOnly(*dbPath); err == nil {
				return store, func() { store.Close() }, nil
			}
			time.Sleep(150 * time.Millisecond)
		}
		return nil, nil, err
	}

	absOut, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	if fi, err := os.Stat(absOut); err != nil || !fi.IsDir() {
		log.Fatalf("out dir %s not found", *out)
	}

	// Open the database once in read-write mode at startup: this creates it if
	// missing and, more importantly, replays/checkpoints a leftover write-ahead
	// log (e.g. after a crawl or list import was interrupted). Read-only opens
	// cannot replay a WAL, so without this the dashboard would serve nothing.
	if _, statErr := os.Stat(*dbPath); statErr != nil {
		if s, err := harvestdb.Open(*dbPath); err == nil {
			s.Close()
		} else {
			log.Printf("could not create %s: %v", *dbPath, err)
		}
	} else if _, walErr := os.Stat(*dbPath + ".wal"); walErr == nil {
		log.Printf("found %s.wal, replaying and checkpointing", *dbPath)
		if s, err := harvestdb.Open(*dbPath); err == nil {
			s.Close()
			log.Printf("wal replayed")
		} else {
			log.Printf("wal replay failed: %v", err)
		}
	}

	absDB, err := filepath.Abs(*dbPath)
	if err != nil {
		log.Fatal(err)
	}

	// crawl settings come from the environment (systemd EnvironmentFile), so the
	// dashboard can show the exact configuration the crawls run with
	meta := func() web.Meta {
		var size int64
		if fi, err := os.Stat(absDB); err == nil {
			size = fi.Size()
		}
		return web.Meta{
			DBPath:    absDB,
			DBSize:    size,
			MediaRoot: absOut,
			Config: map[string]string{
				"depth":          env("DEPTH", "2"),
				"min_res":        env("MIN_RES", "600x400"),
				"quality":        env("QUALITY", "92"),
				"max_pages":      env("MAX_PAGES", "200"),
				"workers":        env("WORKERS", "4"),
				"discover_limit": env("DISCOVER_LIMIT", "2000"),
			},
		}
	}

	r := web.NewRouter(web.Opener(open), absOut, meta)
	webui.Register(r)
	log.Printf("harvest-web on %s (db=%s out=%s)", *addr, *dbPath, absOut)
	if err := r.Run(*addr); err != nil {
		log.Fatal(err)
	}
}

// env returns the environment value or a fallback.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
