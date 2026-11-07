// harvest-web serves the crawl dashboard: run metadata, image metadata with
// perceptual similarity, and the harvested image files.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

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
		store, err := harvestdb.OpenReadOnly(*dbPath)
		if err != nil {
			return nil, nil, err
		}
		return store, func() { store.Close() }, nil
	}

	absOut, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	if fi, err := os.Stat(absOut); err != nil || !fi.IsDir() {
		log.Fatalf("out dir %s not found", *out)
	}

	// create the database up front so read-only opens always have a file
	if _, statErr := os.Stat(*dbPath); statErr != nil {
		if s, err := harvestdb.Open(*dbPath); err == nil {
			s.Close()
		} else {
			log.Printf("could not create %s: %v", *dbPath, err)
		}
	}

	r := web.NewRouter(web.Opener(open), absOut)
	webui.Register(r)
	log.Printf("harvest-web on %s (db=%s out=%s)", *addr, *dbPath, absOut)
	if err := r.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
