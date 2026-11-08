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
	minRes := flag.String("min", "1200x800", "minimum image resolution WxH — only high-resolution images are downloaded")
	workers := flag.Int("workers", 4, "concurrent fetches")
	maxPages := flag.Int("maxpages", 200, "stop crawling after this many pages")
	out := flag.String("out", ".", "output directory")
	quality := flag.Int("quality", 92, "JPEG quality for webp->jpg conversion")
	interval := flag.Duration("interval", 0, "minimum delay between outgoing requests, e.g. 1s (0 = no pacing)")
	subdomains := flag.Bool("subdomains", false, "follow links on subdomains of the seed host (default: stay on the seed host)")
	domainInterval := flag.Duration("domain-interval", 0, "delay between finishing one domain and starting the next")
	doCrawlDomains := flag.Bool("crawl-domains", false, "crawl every imported domain that has not been crawled yet (rank order), then keep going")
	domainTimeout := flag.Duration("domain-timeout", 0, "abandon a domain after this long, e.g. 10m (0 = no limit)")
	crawlOffset := flag.Int("crawl-offset", -1, "with -crawl-domains: list position to start from (-1 = resume from the saved cursor); the position is remembered across restarts")
	crawlLimit := flag.Int("crawl-limit", 0, "with -crawl-domains: stop after this many domains (0 = all)")
	dbPath := flag.String("db", "harvest.duckdb", "duckdb metadata file (empty disables)")
	rehash := flag.Bool("rehash", false, "backfill perceptual hashes for already-saved images, then exit")
	requireNetFS := flag.Bool("require-netfs", false, "refuse to run unless -out is on a network filesystem (cifs/nfs), so a missing NAS mount cannot fill the local disk")
	importReset := flag.Bool("import-reset", false, "with -import-domains: wipe the domains table first (clean re-import)")
	doImport := flag.Bool("import-domains", false, "parse the domain list (-list) and dump every domain into the db, then exit; no probing")
	doDiscover := flag.Bool("discover", false, "probe candidate domains from a top-N list and rank them in the db, then exit")
	doCategories := flag.Bool("categories", false, "refresh the nipic.com category tree in the db, then exit")
	list := flag.String("list", discover.DefaultList, "domain list: local file path or URL (.zip supported)")
	listCache := flag.String("list-cache", "top-1m.csv", "cache file for downloaded lists")
	refresh := flag.Bool("refresh", false, "re-download the domain list even if cached")
	discoverLimit := flag.Int("discover-limit", 2000, "number of list entries to probe")
	discoverOffset := flag.Int("discover-offset", 0, "skip this many list entries before probing")
	discoverTimeout := flag.Duration("discover-timeout", discover.DefaultTimeout, "per-domain probe timeout (short = fast sweep of the whole list)")
	discoverForce := flag.Bool("discover-force", false, "re-probe domains even if they were probed recently")
	discoverRetry := flag.Duration("discover-retry-after", 14*24*time.Hour, "re-probe a domain only after this long (0 = never re-probe)")
	flag.Parse()

	// request pacing and crawl scope apply to every network-fetching mode
	crawler.SetInterval(*interval)
	crawler.SetSubdomains(*subdomains)
	crawler.SetDomainTimeout(*domainTimeout)

	if *doCrawlDomains {
		if err := crawlDomains(*dbPath, *out, *depth, *minRes, *quality, *maxPages, *workers, *crawlLimit, *crawlOffset, *interval, *domainInterval, *requireNetFS); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if *doImport {
		if err := importDomains(*dbPath, *list, *listCache, *refresh, *importReset); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if *doDiscover {
		if err := discoverDomains(*dbPath, *list, *listCache, *refresh, *discoverLimit, *discoverOffset, *workers, *discoverTimeout, *discoverForce, *discoverRetry); err != nil {
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

// importDomains parses the domain list and stores every domain in DuckDB as a
// candidate. No network I/O: no connectivity tests, no page-content evaluation.
//
// The list is written in batches, each on its own short-lived connection:
// DuckDB holds its file lock for the lifetime of a connection, so this is what
// lets the dashboard keep answering while a million rows are imported.
// crawlDomains crawls the imported domain list, resuming from a saved cursor so
// a restart continues where it stopped instead of skipping ahead. Each finished
// domain is marked in the domains table and the cursor is persisted; between
// domains it waits domainInterval, and every request is paced by -interval.
// Only images at or above -min are downloaded.
func crawlDomains(dbPath, out string, depth int, minRes string, quality, maxPages, workers, limit, offset int, interval, domainInterval time.Duration, requireNetFS bool) error {
	w, h, err := parseRes(minRes)
	if err != nil {
		return fmt.Errorf("bad -min: %w", err)
	}
	if requireNetFS {
		if err := checkNetFS(out); err != nil {
			return err
		}
	}

	// resume position: an explicit -crawl-offset wins and becomes the saved
	// cursor, otherwise continue where the last run stopped
	store, err := openWithRetry(dbPath, 20)
	if err != nil {
		return err
	}
	cursor, err := store.Cursor()
	if err != nil {
		store.Close()
		return err
	}
	if offset >= 0 {
		cursor = int64(offset) // list positions are 0-based: offset N starts at rank N+1
		if err := store.SetCursor(cursor); err != nil {
			store.Close()
			return err
		}
		fmt.Printf("cursor set to %d (starting at list rank %d)\n", cursor, cursor+1)
	}
	pendingCount, err := store.CountToCrawl()
	store.Close()
	if err != nil {
		return err
	}

	fmt.Printf("crawl plan: cursor=%d pending=%d depth=%d min=%dx%d quality=%d maxpages=%d workers=%d interval=%s domain-interval=%s domain-timeout=%s\n",
		cursor, pendingCount, depth, w, h, quality, maxPages, workers, interval, domainInterval, crawler.DomainTimeout())

	processed := 0
	for {
		if limit > 0 && processed >= limit {
			fmt.Printf("stopping: reached -crawl-limit %d\n", limit)
			return nil
		}

		// next batch of un-crawled domains at or after the cursor
		store, err := openWithRetry(dbPath, 20)
		if err != nil {
			return err
		}
		batch, err := store.PendingDomains(cursor, 50)
		store.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			fmt.Printf("done: no pending domains left from cursor %d\n", cursor)
			return nil
		}

		for _, row := range batch {
			if limit > 0 && processed >= limit {
				fmt.Printf("stopping: reached -crawl-limit %d\n", limit)
				return nil
			}
			processed++
			started := time.Now()
			fmt.Printf("[rank %d #%d] %s %s\n", row.ID, processed, row.Domain, started.Format("15:04:05"))

			res, crawlErr := crawler.Run(row.Domain, depth, w, h, workers, maxPages, quality, out)
			if crawlErr != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", row.Domain, crawlErr)
			} else {
				took := time.Since(started).Seconds()
				t := res.Timing
				other := took - t.WaitS - t.BackoffS - t.ReqS - t.BodyS
				if other < 0 {
					other = 0
				}
				fmt.Printf("  pages=%d candidates=%d saved=%d skipped=%d dir=%s (%.0fs)\n",
					res.Pages, res.Candidates, res.Saved, res.Skipped, res.Dir, took)
				fmt.Printf("  timing: %.0fs = pace %.1fs + backoff %.1fs + http %.1fs + body %.1fs + other %.1fs | requests %d (200:%d blocked:%d err:%d)\n",
					took, t.WaitS, t.BackoffS, t.ReqS, t.BodyS, other, t.Requests, t.OK, t.Blocked, t.Errors)
			}

			// record the run, mark the domain and advance the cursor — one short
			// connection, so the dashboard keeps reading the same file
			next := row.ID + 1
			if s, err := openWithRetry(dbPath, 20); err == nil {
				if _, err := s.InsertRun(harvestdb.Run{
					Domain:           row.Domain,
					StartedAt:        started.Format("2006-01-02 15:04:05"),
					DurationS:        time.Since(started).Seconds(),
					Depth:            depth,
					PagesVisited:     res.Pages,
					ImagesDownloaded: res.Saved,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "  db run: %v\n", err)
				}
				if err := s.MarkDomainCrawled(row.Domain, time.Now().Format("2006-01-02 15:04:05"), res.Saved); err != nil {
					fmt.Fprintf(os.Stderr, "  db mark: %v\n", err)
				}
				if err := s.SetCursor(next); err != nil {
					fmt.Fprintf(os.Stderr, "  db cursor: %v\n", err)
				}
				s.Close()
			} else {
				fmt.Fprintf(os.Stderr, "  db open: %v\n", err)
			}
			cursor = next

			if domainInterval > 0 {
				fmt.Printf("  cursor=%d, waiting %s before the next domain\n", cursor, domainInterval)
				time.Sleep(domainInterval)
			}
		}
	}
}

func importDomains(dbPath, list, cachePath string, refresh, reset bool) error {
	domains, err := discover.LoadList(list, cachePath, refresh)
	if err != nil {
		return err
	}
	fmt.Printf("parsed %d domains from %s\n", len(domains), list)

	start := time.Now()
	stamp := time.Now().Format("2006-01-02 15:04:05")

	if reset {
		s, err := openWithRetry(dbPath, 20)
		if err != nil {
			return err
		}
		err = s.ClearDomains()
		s.Close()
		if err != nil {
			return err
		}
		fmt.Println("cleared existing domain rows")
	}

	// filter out what is already stored (one short read)
	known := map[string]bool{}
	{
		s, err := openWithRetry(dbPath, 20)
		if err != nil {
			return err
		}
		if known, err = s.KnownDomains(); err != nil {
			s.Close()
			return err
		}
		s.Close()
	}
	fresh := make([]string, 0, len(domains))
	seen := make(map[string]bool, len(domains))
	for _, d := range domains {
		if d == "" || known[d] || seen[d] {
			continue
		}
		seen[d] = true
		fresh = append(fresh, d)
	}
	fmt.Printf("%d new domains to import (%d already present)\n", len(fresh), len(domains)-len(fresh))

	// small batches: the write lock is held for the lifetime of a connection, so
	// each batch must be short enough for the dashboard to slip in between
	const batchSize = 1000
	added := 0
	for off := 0; off < len(fresh); off += batchSize {
		end := min(off+batchSize, len(fresh))
		s, err := openWithRetry(dbPath, 20)
		if err != nil {
			return err
		}
		n, err := s.InsertDomains(fresh[off:end], "top-1m", stamp)
		s.Close() // releases DuckDB's write lock for the next reader
		if err != nil {
			return err
		}
		added += n
		if added%50000 == 0 || end == len(fresh) {
			fmt.Printf("  imported %d/%d (%.0fs)\n", added, len(fresh), time.Since(start).Seconds())
		}
		time.Sleep(10 * time.Millisecond)
	}

	var total int64
	if s, err := openWithRetry(dbPath, 20); err == nil {
		total, _ = s.CountDomains()
		s.Close()
	}
	fmt.Printf("imported %d new domains in %.0fs; %d candidates in %s\n",
		added, time.Since(start).Seconds(), total, dbPath)
	return nil
}

func discoverDomains(dbPath, list, cachePath string, refresh bool, limit, offset, workers int, timeout time.Duration, force bool, retryAfter time.Duration) error {
	// no long-lived store here: the probing phase must not hold the DuckDB lock
	if s, err := openWithRetry(dbPath, 5); err != nil {
		return err
	} else {
		s.Close() // make sure the schema exists before the first batch write
	}
	cands, err := discover.LoadList(list, cachePath, refresh)
	if err != nil {
		return err
	}
	fmt.Printf("loaded %d candidate domains\n", len(cands))

	// A domain is skipped when it was probed recently (probe ledger) or is a
	// stored candidate. That is what makes repeated sweeps of the whole 1M list
	// affordable: only new entries and entries past retryAfter are probed.
	known := map[string]bool(nil)
	if !force {
		s, err := openWithRetry(dbPath, 20)
		if err != nil {
			return err
		}
		since := time.Now().Add(-retryAfter).Format("2006-01-02 15:04:05")
		known, err = s.ProbedSince(since)
		if err == nil {
			var candsKnown map[string]bool
			if candsKnown, err = s.KnownDomains(); err == nil {
				for d := range candsKnown {
					known[d] = true
				}
			}
		}
		s.Close()
		if err != nil {
			return err
		}
		fmt.Printf("skipping %d domains probed in the last %s\n", len(known), retryAfter)
	}

	// write in batches, each on its own short-lived connection, so the dashboard
	// can keep reading the same DuckDB file while discovery runs. Reachable
	// domains become candidates; every attempt goes into the probe ledger.
	sink := func(batch []harvestdb.Domain) error {
		s, err := openWithRetry(dbPath, 20)
		if err != nil {
			return err
		}
		defer s.Close()
		probes := make([]harvestdb.Probe, 0, len(batch))
		for _, d := range batch {
			if discover.Reachable(d) {
				if _, err := s.UpsertDomain(d); err != nil {
					return err
				}
			}
			probes = append(probes, harvestdb.Probe{
				Domain: d.Domain, Reachable: discover.Reachable(d), LastProbed: d.LastChecked,
			})
		}
		return s.RecordProbes(probes)
	}

	domains, err := discover.Run(sink, cands, discover.Options{
		Workers: workers, Limit: limit, Offset: offset, Timeout: timeout, Known: known,
	})
	if err != nil {
		return err
	}

	reachable := 0
	for _, d := range domains {
		if discover.Reachable(d) {
			reachable++
		}
	}
	top := min(20, len(domains))
	fmt.Printf("\n%d reachable candidates stored in %s (probe timeout %s); top %d ranked:\n",
		reachable, dbPath, timeout, top)
	for i := 0; i < top; i++ {
		d := domains[i]
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
