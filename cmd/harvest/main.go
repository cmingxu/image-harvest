// image-harvest crawls a site up to a link depth, downloads jpg/png/webp
// images meeting a minimum resolution, converts webp to jpg, and records
// crawl metadata in DuckDB.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"image-harvest/internal/crawler"
	"image-harvest/internal/discover"
	"image-harvest/internal/harvestdb"
	"image-harvest/internal/nipic"
)

func main() {
	depth := flag.Int("depth", 2, "link depth to crawl (0 = seed page only)")
	minRes := flag.String("min", "0x0", "minimum image resolution WxH")
	workers := flag.Int("workers", 4, "concurrent fetches")
	maxPages := flag.Int("maxpages", 500, "stop crawling after this many pages")
	out := flag.String("out", ".", "output directory")
	quality := flag.Int("quality", 85, "JPEG quality for webp->jpg conversion")
	dbPath := flag.String("db", "harvest.duckdb", "duckdb metadata file (empty disables)")
	rehash := flag.Bool("rehash", false, "backfill perceptual hashes for already-saved images, then exit")
	requireNetFS := flag.Bool("require-netfs", false, "refuse to run unless -out is on a network filesystem (cifs/nfs), so a missing NAS mount cannot fill the local disk")
	doDiscover := flag.Bool("discover", false, "probe candidate domains from a top-N list and rank them in the db, then exit")
	doCategories := flag.Bool("categories", false, "refresh the nipic.com category tree in the db, then exit")
	list := flag.String("list", discover.DefaultList, "domain list: local file path or URL (.zip supported)")
	listCache := flag.String("list-cache", "top-1m.csv", "cache file for downloaded lists")
	refresh := flag.Bool("refresh", false, "re-download the domain list even if cached")
	discoverLimit := flag.Int("discover-limit", 2000, "number of list entries to probe")
	discoverOffset := flag.Int("discover-offset", 0, "skip this many list entries before probing")
	flag.Parse()

	if *doDiscover {
		if err := discoverDomains(*dbPath, *list, *listCache, *refresh, *discoverLimit, *discoverOffset, *workers); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if *doCategories {
		if err := refreshCategories(*dbPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if *rehash {
		if err := rehashSaved(*dbPath, *out); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: image-harvest [-depth 2] [-min 800x600] [-workers 4] [-maxpages 500] [-out .] [-quality 85] [-db harvest.duckdb] <domain>")
		os.Exit(2)
	}
	w, h, err := parseRes(*minRes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad -min:", err)
		os.Exit(2)
	}

	if *requireNetFS {
		if err := checkNetFS(*out); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}

	res, err := crawler.Run(flag.Arg(0), *depth, w, h, *workers, *maxPages, *quality, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Printf("crawled %d pages, found %d unique image candidates\n", res.Pages, res.Candidates)
	fmt.Printf("saved %d to %s, skipped %d (%ds)\n", res.Saved, res.Dir, res.Skipped, int(res.Duration.Seconds()))

	// The database is opened only now: a crawl holds no lock while it runs, so a
	// dashboard serving the same file stays responsive. Opening can still lose a
	// race with a dashboard request, hence the short retry loop.
	var store *harvestdb.Store
	if *dbPath != "" {
		if s, err := openWithRetry(*dbPath, 20); err != nil {
			fmt.Fprintf(os.Stderr, "warn: duckdb unavailable (%v); skipping metadata\n", err)
		} else {
			store = s
			defer store.Close()
		}
	}

	if store != nil {
		runID, err := store.InsertRun(harvestdb.Run{
			Domain:           res.SeedHost,
			StartedAt:        res.Start.Format("2006-01-02 15:04:05"),
			DurationS:        res.Duration.Seconds(),
			Depth:            res.Depth,
			PagesVisited:     res.Pages,
			ImagesDownloaded: res.Saved,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: db insert run: %v\n", err)
		} else if err := store.InsertImages(runID, res.Images); err != nil {
			fmt.Fprintf(os.Stderr, "warn: db insert images: %v\n", err)
		} else {
			fmt.Printf("metadata: run #%d and %d images stored in %s\n", runID, len(res.Images), *dbPath)
		}
	}
}

func refreshCategories(dbPath string) error {
	store, err := harvestdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	for kind, page := range nipic.Pages {
		roots, err := nipic.Fetch(kind, page)
		if err != nil {
			return err
		}
		if err := store.ReplaceCategories(kind, roots); err != nil {
			return err
		}
		children := 0
		for _, r := range roots {
			children += len(r.Children)
		}
		fmt.Printf("%s: %d categories, %d subcategories\n", kind, len(roots), children)
	}
	return nil
}

func discoverDomains(dbPath, list, cachePath string, refresh bool, limit, offset, workers int) error {
	store, err := harvestdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	cands, err := discover.LoadList(list, cachePath, refresh)
	if err != nil {
		return err
	}
	fmt.Printf("loaded %d candidate domains\n", len(cands))
	domains, err := discover.Run(store, cands, workers, limit, offset)
	if err != nil {
		return err
	}
	fmt.Printf("\ntop %d of %d ranked domains:\n", min(20, len(domains)), len(domains))
	for i, d := range domains[:min(20, len(domains))] {
		fmt.Printf("%2d. %-28s score=%6.1f density=%-3d hi-res=%2d%% %s\n",
			i+1, d.Domain, d.Score, d.ImgDensity, int(d.HiResRatio*100), d.Note)
	}
	return nil
}

func rehashSaved(dbPath, out string) error {
	store, err := harvestdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	hashed, err := crawler.RehashSaved(store, out)
	if err != nil {
		return err
	}
	fmt.Printf("rehashed %d images in %s\n", hashed, dbPath)
	return nil
}

func parseRes(s string) (int, int, error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected WxH, got %q", s)
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w < 0 || h < 0 {
		return 0, 0, fmt.Errorf("expected WxH, got %q", s)
	}
	return w, h, nil
}

// openWithRetry opens the DuckDB store, retrying briefly: a dashboard request
// against the same file holds the lock for a few milliseconds at a time.
func openWithRetry(path string, attempts int) (*harvestdb.Store, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var s *harvestdb.Store
		if s, err = harvestdb.Open(path); err == nil {
			return s, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, err
}

// netFS is the set of filesystem types that count as "the NAS is mounted".
var netFS = map[string]bool{"cifs": true, "smb3": true, "nfs": true, "nfs4": true}

// checkNetFS verifies that out (or its nearest existing ancestor) sits on a
// network filesystem. It exists so a forgotten mount cannot silently write the
// harvested images onto the local disk.
func checkNetFS(out string) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	probe := abs
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	b, err := exec.Command("findmnt", "-n", "-o", "FSTYPE,TARGET", "-T", probe).Output()
	if err != nil {
		return fmt.Errorf("cannot determine filesystem of %s: %v", probe, err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return fmt.Errorf("cannot determine filesystem of %s", probe)
	}
	if !netFS[fields[0]] {
		return fmt.Errorf("%s is on %q (%s) — refusing to write harvested images to local disk; mount the NAS first",
			abs, fields[0], fields[1])
	}
	fmt.Printf("media root %s on %s (%s)\n", abs, fields[1], fields[0])
	return nil
}
