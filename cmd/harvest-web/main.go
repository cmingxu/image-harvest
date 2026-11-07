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

	store, err := harvestdb.Open(*dbPath)
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	defer store.Close()

	absOut, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	if fi, err := os.Stat(absOut); err != nil || !fi.IsDir() {
		log.Fatalf("out dir %s not found", *out)
	}

	r := web.NewRouter(store, absOut)
	webui.Register(r)
	log.Printf("harvest-web on %s (db=%s out=%s)", *addr, *dbPath, absOut)
	if err := r.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
