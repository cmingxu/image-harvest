// Package crawler walks a site up to a link depth, downloads jpg/png/webp
// images meeting a minimum resolution, converts webp to jpg, saves them to
// <out>/YYYY-MM-DD-HH/, and computes a perceptual hash for each saved image.
package crawler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register png decoder for DecodeConfig
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/webp"
	"golang.org/x/net/html"

	"image-harvest/internal/harvestdb"
	"image-harvest/internal/phash"
)

var (
	client        = newClient()
	allowedCT     = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
	dialTimeout   = 5 * time.Second  // TCP connect (blackholed hosts fail fast)
	tlsTimeout    = 6 * time.Second  // TLS handshake
	headerTimeout = 12 * time.Second // first byte, per request
	pageLimit     = int64(50 << 20)  // cap HTML bodies
	imageLimit    = int64(50 << 20)  // cap image bodies
	// realistic browser UA; Go's default "Go-http-client/1.1" gets blocked by many CDNs
	ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	// service subdomains that rarely carry site content worth crawling
	serviceLabels = map[string]bool{
		"mail": true, "webmail": true, "mx": true, "smtp": true, "pop": true, "pop3": true,
		"imap": true, "exchange": true, "owa": true, "ftp": true, "sftp": true, "ns": true,
		"ns1": true, "ns2": true, "ns3": true, "dns": true, "vpn": true, "stun": true,
		"sip": true, "api": true, "oauth": true, "auth": true, "login": true,
		"cpanel": true, "whm": true, "webmin": true,
	}
)

// Result reports what one crawl run did.
type Result struct {
	SeedHost   string
	Start      time.Time
	Duration   time.Duration
	Depth      int
	Pages      int
	Candidates int
	Saved      int
	Skipped    int
	Images     []harvestdb.Image
	Dir        string
	Timing     Timing
}

// Run crawls domain up to depth link levels and downloads matching images
// into a fresh YYYY-MM-DD-HH folder under out.
func Run(domain string, depth, minW, minH, workers, maxPages, quality int, out string) (Result, error) {
	seed, err := seedURL(domain)
	if err != nil {
		return Result{}, err
	}
	start := time.Now()
	resetTiming()
	resetProgress()

	// The output directory is created up front and images are downloaded page by
	// page as they are discovered, so files keep landing during a long crawl
	// instead of only after the whole BFS finishes.
	dir, err := pickDir(out)
	if err != nil {
		return Result{}, err
	}
	relBase := filepath.Base(dir)

	var mu sync.Mutex
	var files atomic.Uint32 // shared file counter for the whole run
	saved, skipped, candidates := 0, 0, 0
	var recs []harvestdb.Image

	emit := func(urls []string) {
		mu.Lock()
		candidates += len(urls)
		mu.Unlock()
		bodyStart := time.Now()
		s, sk, r := downloadAll(urls, dir, relBase, minW, minH, quality, workers, &files)
		timing.bodyNs.Add(int64(time.Since(bodyStart)))
		mu.Lock()
		saved += s
		skipped += sk
		recs = append(recs, r...)
		mu.Unlock()
		emitImages(r) // persist rows now, not at the end of the domain
	}

	pages := crawl(seed, depth, workers, maxPages, emit)

	return Result{
		SeedHost:   seed.Host,
		Start:      start,
		Duration:   time.Since(start),
		Depth:      depth,
		Pages:      pages,
		Candidates: candidates,
		Saved:      saved,
		Skipped:    skipped,
		Images:     recs,
		Dir:        dir,
		Timing:     timingSnapshot(time.Since(start)),
	}, nil
}

// pickDir creates a fresh YYYY-MM-DD-HH(-N) output directory for one run.
func pickDir(out string) (string, error) {
	stamp := time.Now().Format("2006-01-02-15")
	dir := filepath.Join(out, stamp)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(out, fmt.Sprintf("%s-%d", stamp, i))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil) // keep cookies across requests (consent walls, sessions)
	// Split the timeouts: a blocked/blackholed host must fail on connect in a
	// few seconds instead of burning the whole 15s request budget three times.
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: headerTimeout,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: tr,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// --- timing breakdown -----------------------------------------------------
//
// Every request goes through Get, so that is where the wall clock of a crawl
// actually goes: pacing sleeps (-interval), retry backoff (403/429/5xx), the
// HTTP exchange itself, and body reads / decoding. These counters make the
// split visible per domain.
type Timing struct {
	Requests int64   // HTTP attempts, retries included
	OK       int64   // 200 responses
	Blocked  int64   // 403/429/5xx
	Errors   int64   // transport/timeout errors
	WaitS    float64 // sleeping to honor -interval
	BackoffS float64 // sleeping between retries
	ReqS     float64 // HTTP exchange (headers)
	BodyS    float64 // body read + decode + save
}

type timingCounters struct {
	requests, ok, blocked, errs      atomic.Int64
	waitNs, backoffNs, reqNs, bodyNs atomic.Int64
}

var timing timingCounters

func resetTiming() {
	timing.requests.Store(0)
	timing.ok.Store(0)
	timing.blocked.Store(0)
	timing.errs.Store(0)
	timing.waitNs.Store(0)
	timing.backoffNs.Store(0)
	timing.reqNs.Store(0)
	timing.bodyNs.Store(0)
}

func timingSnapshot(total time.Duration) Timing {
	sec := func(ns *atomic.Int64) float64 { return float64(ns.Load()) / 1e9 }
	return Timing{
		Requests: timing.requests.Load(),
		OK:       timing.ok.Load(),
		Blocked:  timing.blocked.Load(),
		Errors:   timing.errs.Load(),
		WaitS:    sec(&timing.waitNs),
		BackoffS: sec(&timing.backoffNs),
		ReqS:     sec(&timing.reqNs),
		BodyS:    sec(&timing.bodyNs),
	}
}

// --- request pacing -------------------------------------------------------
//
// A single crawl shares one rate limit: every outgoing request (page fetch,
// image download, retry) waits until the interval has elapsed since the
// previous one. Set with SetInterval from the CLI (-interval).
var (
	reqMu       sync.Mutex
	reqInterval time.Duration
	lastRequest time.Time
)

// SetInterval enforces a minimum delay between outgoing HTTP requests.
// Zero disables pacing.
func SetInterval(d time.Duration) {
	reqMu.Lock()
	reqInterval = d
	reqMu.Unlock()
}

// --- crawl scope ----------------------------------------------------------

var (
	scopeMu sync.RWMutex
	// off by default: crawling stays on the seed host and never enumerates
	// subdomains (opt in with -subdomains)
	allowSubdomains = false
)

// SetSubdomains controls whether the crawl follows links on subdomains of the
// seed host (news.example.com) or stays on the host it started on.
func SetSubdomains(allow bool) {
	scopeMu.Lock()
	allowSubdomains = allow
	scopeMu.Unlock()
}

// Subdomains reports the current crawl scope.
func Subdomains() bool {
	scopeMu.RLock()
	defer scopeMu.RUnlock()
	return allowSubdomains
}

// --- live progress --------------------------------------------------------

var (
	progressPages atomic.Int64
)

func resetProgress() { progressPages.Store(0) }

// Progress returns how many pages the current crawl has fetched so far, so the
// run row can be updated while the domain is still being crawled.
func Progress() int { return int(progressPages.Load()) }

// --- incremental image sink -----------------------------------------------

var (
	sinkMu    sync.RWMutex
	imageSink func([]harvestdb.Image)
)

// SetImageSink installs a callback that receives each batch of image records as
// soon as it is saved, so metadata becomes visible during a long crawl instead
// of only when the domain finishes. nil disables it.
func SetImageSink(f func([]harvestdb.Image)) {
	sinkMu.Lock()
	imageSink = f
	sinkMu.Unlock()
}

func emitImages(imgs []harvestdb.Image) {
	sinkMu.RLock()
	f := imageSink
	sinkMu.RUnlock()
	if f != nil && len(imgs) > 0 {
		f(imgs)
	}
}

// --- per-request debug log ------------------------------------------------

var (
	debugMu       sync.RWMutex
	debugRequests bool
)

// SetDebugRequests turns on one log line per HTTP request (URL, attempt, status,
// duration, pacing wait, bytes) plus the verdict for every image candidate.
func SetDebugRequests(on bool) {
	debugMu.Lock()
	debugRequests = on
	debugMu.Unlock()
}

// DebugRequests reports whether per-request logging is on.
func DebugRequests() bool {
	debugMu.RLock()
	defer debugMu.RUnlock()
	return debugRequests
}

// dbgf logs when -debug-requests is enabled; stderr goes to the service log.
func dbgf(format string, args ...any) {
	if DebugRequests() {
		log.Printf(format, args...)
	}
}

// --- per-domain budget ----------------------------------------------------

var (
	domainTimeoutMu sync.RWMutex
	domainTimeout   time.Duration
)

// SetDomainTimeout bounds how long one domain may be crawled (0 = unlimited).
// It keeps a huge or bot-walled site from monopolizing the queue.
func SetDomainTimeout(d time.Duration) {
	domainTimeoutMu.Lock()
	domainTimeout = d
	domainTimeoutMu.Unlock()
}

// DomainTimeout reports the per-domain budget.
func DomainTimeout() time.Duration {
	domainTimeoutMu.RLock()
	defer domainTimeoutMu.RUnlock()
	return domainTimeout
}

// inScope reports whether a link host may be crawled. With subdomains disabled
// only the seed host itself (www. normalized) is followed.
func inScope(host, seedHost string) bool {
	if Subdomains() {
		return sameSite(host, seedHost)
	}
	return strings.TrimPrefix(host, "www.") == seedHost
}

// Interval reports the current request pacing.
func Interval() time.Duration {
	reqMu.Lock()
	defer reqMu.Unlock()
	return reqInterval
}

// waitTurn blocks until this request may go out. The lock is held across the
// sleep on purpose: concurrent workers must queue up, otherwise two of them
// would sleep for the same window and fire together, doubling the rate.
func waitTurn() {
	reqMu.Lock()
	defer reqMu.Unlock()
	if reqInterval <= 0 {
		return
	}
	if !lastRequest.IsZero() {
		if wait := reqInterval - time.Since(lastRequest); wait > 0 {
			time.Sleep(wait)
		}
	}
	lastRequest = time.Now()
}

// Get fetches a URL, logging it as an unclassified request.
func Get(u string) (*http.Response, error) { return get(u, "request") }

// get fetches a URL with browser-like headers, retrying up to 3 times with
// backoff on 403/429/5xx (the usual bot-protection statuses). Transport errors
// are retried once at most, and timeouts not at all: a blackholed host will not
// answer on the second attempt either, and each attempt costs seconds.
func get(u, kind string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		paceStart := time.Now()
		waitTurn() // honor -interval before every request, retries included
		paceWait := time.Since(paceStart)
		timing.waitNs.Add(int64(paceWait))
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		timing.requests.Add(1)
		reqStart := time.Now()
		resp, err := client.Do(req)
		reqDur := time.Since(reqStart)
		timing.reqNs.Add(int64(reqDur))
		if err != nil {
			timing.errs.Add(1)
			dbgf("req %-5s attempt=%d status=ERR dur=%s pace=%s err=%v url=%s",
				kind, attempt+1, reqDur.Round(time.Millisecond), paceWait.Round(time.Millisecond), err, u)
			lastErr = err
			if attempt > 0 || isTimeoutErr(err) {
				break // host is blocked/unreachable: don't pay for more attempts
			}
			timing.backoffNs.Add(int64(time.Second))
			time.Sleep(time.Second)
			continue
		}
		dbgf("req %-5s attempt=%d status=%d dur=%s pace=%s len=%d url=%s",
			kind, attempt+1, resp.StatusCode, reqDur.Round(time.Millisecond), paceWait.Round(time.Millisecond),
			resp.ContentLength, u)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			wait := time.Duration(1<<attempt) * time.Second // 1s, 2s, 4s
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			timing.blocked.Add(1)
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			resp.Body.Close()
			timing.backoffNs.Add(int64(wait))
			time.Sleep(wait)
			continue
		}
		timing.ok.Add(1)
		return resp, nil
	}
	return nil, lastErr
}

// isTimeoutErr reports whether err is a connect/read timeout (blocked host).
func isTimeoutErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "i/o timeout")
}

// crawl BFSes from seed up to depth link levels. Follows links on the seed
// domain and its subdomains (ifeng.com links to news.ifeng.com, etc), but
// guesses which subdomains are worth entering: known service subdomains are
// skipped entirely, and a fresh subdomain whose first page has no links and
// no images is marked dead and abandoned.
func crawl(seed *url.URL, depth, workers, maxPages int, emit func([]string)) int {
	seedHost := strings.TrimPrefix(seed.Hostname(), "www.")
	visited := map[string]bool{seed.String(): true}
	seenImgs := map[string]bool{}
	seenHost := map[string]bool{}
	deadHosts := map[string]bool{}

	var deadline time.Time
	if d := DomainTimeout(); d > 0 {
		deadline = time.Now().Add(d)
	}
	expired := func() bool { return !deadline.IsZero() && time.Now().After(deadline) }

	queue := []string{seed.String()}
	var mu sync.Mutex
	pages := 0
	stop := false

	for level := 0; level <= depth && len(queue) > 0 && !stop; level++ {
		var next []string
		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for _, u := range queue {
			wg.Add(1)
			sem <- struct{}{}
			go func(u string) {
				defer wg.Done()
				defer func() { <-sem }()
				h := hostOf(u)
				mu.Lock()
				if stop || deadHosts[h] || pages >= maxPages || expired() {
					mu.Unlock()
					return
				}
				mu.Unlock()

				links, pageImgs := FetchPage(u)

				mu.Lock()
				pages++
				progressPages.Store(int64(pages))
				if !seenHost[h] {
					seenHost[h] = true
					// a content-free first page means a service shell: abandon host
					if len(links) == 0 && len(pageImgs) == 0 {
						deadHosts[h] = true
					}
				}
				for _, l := range links {
					if !inScope(hostOf(l), seedHost) {
						continue
					}
					if !deadHosts[h] && !visited[l] {
						visited[l] = true
						next = append(next, l)
					}
				}
				var fresh []string
				if !expired() {
					for _, img := range pageImgs {
						if !seenImgs[img] {
							seenImgs[img] = true
							fresh = append(fresh, img)
						}
					}
				}
				if expired() {
					stop = true
				}
				mu.Unlock()

				if len(fresh) > 0 {
					emit(fresh) // download this page's images right away
				}
			}(u)
		}
		wg.Wait()
		queue = next
	}
	return pages
}

func hostOf(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		return parsed.Hostname()
	}
	return u
}

// fetchPage returns same-host links and absolute image URLs found on a page.
func FetchPage(u string) (links, imgs []string) {
	resp, err := get(u, "page")
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, pageLimit))
	if err != nil {
		return nil, nil
	}
	base := resp.Request.URL                            // final URL after redirects; links on ifeng.com 302 to www.ifeng.com
	host := strings.TrimPrefix(base.Hostname(), "www.") // match seed domain + subdomains
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "a":
				if abs := resolve(base, attr(n, "href")); abs != nil && inScope(abs.Hostname(), host) && !serviceSubdomain(abs.Hostname(), host) {
					links = append(links, abs.String())
				}
			case "img":
				if abs := resolve(base, attr(n, "src")); abs != nil && !serviceSubdomain(abs.Hostname(), host) {
					imgs = append(imgs, abs.String())
				}
				for _, cand := range strings.Split(attr(n, "srcset"), ",") {
					if f := strings.Fields(cand); len(f) > 0 {
						if abs := resolve(base, f[0]); abs != nil && !serviceSubdomain(abs.Hostname(), host) {
							imgs = append(imgs, abs.String())
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return links, imgs
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func resolve(base *url.URL, ref string) *url.URL {
	if ref == "" {
		return nil
	}
	abs, err := base.Parse(ref)
	if err != nil {
		return nil
	}
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return nil // skip data:, mailto:, javascript:, ...
	}
	abs.Fragment = ""
	return abs
}

// sameSite reports whether host belongs to the seed domain or one of its
// subdomains (www.ifeng.com, news.ifeng.com both match ifeng.com).
func sameSite(host, seedHost string) bool {
	return host == seedHost || strings.HasSuffix(host, "."+seedHost)
}

// serviceSubdomain reports whether host is a service subdomain like
// mail.ifeng.com or api.ifeng.com — rarely worth crawling.
func serviceSubdomain(host, seedHost string) bool {
	if !strings.HasSuffix(host, "."+seedHost) {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimSuffix(host, "."+seedHost), ".")
	return serviceLabels[first]
}

func seedURL(domain string) (*url.URL, error) {
	domain = strings.TrimSpace(domain)
	seed, err := url.Parse(domain)
	if err != nil || seed.Host == "" {
		if seed, err = url.Parse("https://" + domain); err != nil {
			return nil, err
		}
	}
	if seed.Host == "" || (seed.Scheme != "http" && seed.Scheme != "https") {
		return nil, fmt.Errorf("bad domain %q", domain)
	}
	return seed, nil
}

// downloadAll saves a batch of candidate images. counter is shared across the
// batches of one run so file names never collide (001.jpg, 002.jpg, …).
func downloadAll(urls []string, dir, relBase string, minW, minH, quality, workers int, counter *atomic.Uint32) (saved, skipped int, recs []harvestdb.Image) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, workers)
	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			img, err := saveImage(u, relBase, dir, counter.Add(1), minW, minH, quality)
			mu.Lock()
			recs = append(recs, img)
			if err != nil {
				skipped++
			} else {
				saved++
			}
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return saved, skipped, recs
}

// saveImage downloads u, checks type/dimensions, converts webp->jpg, writes
// <dir>/<idx><ext>, and reports the image's metadata + perceptual hash.
func saveImage(u, relBase, dir string, idx uint32, minW, minH, quality int) (harvestdb.Image, error) {
	img := harvestdb.Image{URL: u}
	resp, err := get(u, "image")
	if err != nil {
		img.Skipped = err.Error()
		dbgf("img  skip err=%v url=%s", err, u)
		return img, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		img.Skipped = fmt.Sprintf("status %d", resp.StatusCode)
		return img, fmt.Errorf("%s", img.Skipped)
	}
	ct := resp.Header.Get("Content-Type")
	ext := strings.ToLower(filepath.Ext(strings.SplitN(u, "?", 2)[0]))
	if !allowedCT[ct] && !(ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp") {
		img.Skipped = fmt.Sprintf("not a supported image (%s, %s)", ct, ext)
		return img, fmt.Errorf("%s", img.Skipped)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageLimit))
	if err != nil {
		img.Skipped = err.Error()
		return img, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		img.Skipped = err.Error()
		return img, err
	}
	img.Width, img.Height = cfg.Width, cfg.Height
	if cfg.Width < minW || cfg.Height < minH {
		img.Skipped = fmt.Sprintf("%dx%d below minimum", cfg.Width, cfg.Height)
		dbgf("img  skip %dx%d < %dx%d url=%s", cfg.Width, cfg.Height, minW, minH, u)
		return img, fmt.Errorf("%s", img.Skipped)
	}
	if format == "webp" {
		decoded, err := webp.Decode(bytes.NewReader(data))
		if err != nil {
			img.Skipped = err.Error()
			return img, err
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, decoded, &jpeg.Options{Quality: quality}); err != nil {
			img.Skipped = err.Error()
			return img, err
		}
		data, ext = buf.Bytes(), ".jpg"
	} else if format == "jpeg" {
		ext = ".jpg"
	} else {
		ext = ".png"
	}
	name := fmt.Sprintf("%03d%s", idx, ext)
	img.Ext = ext
	img.SavedPath = filepath.Join(relBase, name)
	img.SizeBytes = int64(len(data))
	if decoded, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		img.Phash = int64(phash.DHash(decoded))
		img.Vhash = int64(phash.DHashVertical(decoded))
	}
	writeErr := os.WriteFile(filepath.Join(dir, name), data, 0o644)
	if writeErr == nil {
		dbgf("img  saved %dx%d %dKB name=%s url=%s", cfg.Width, cfg.Height, len(data)/1024, img.SavedPath, u)
	} else {
		dbgf("img  write-failed err=%v url=%s", writeErr, u)
	}
	return img, writeErr
}

// RehashSaved backfills perceptual hashes for saved images whose phash is
// still unset, by decoding the files under out. Returns the number hashed.
func RehashSaved(store *harvestdb.Store, out string) (int, error) {
	imgs, err := store.SavedImages()
	if err != nil {
		return 0, err
	}
	hashed := 0
	for _, im := range imgs {
		if im.Phash != 0 && im.Vhash != 0 {
			continue // both hashes present
		}
		data, err := os.ReadFile(filepath.Join(out, im.SavedPath))
		if err != nil {
			continue // file missing; skip
		}
		m, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		if err := store.SetPhash(im.ID, int64(phash.DHash(m)), int64(phash.DHashVertical(m))); err != nil {
			return hashed, err
		}
		hashed++
	}
	return hashed, nil
}
