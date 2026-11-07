// Package crawler walks a site up to a link depth, downloads jpg/png/webp
// images meeting a minimum resolution, converts webp to jpg, saves them to
// <out>/YYYY-MM-DD-HH/, and computes a perceptual hash for each saved image.
package crawler

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register png decoder for DecodeConfig
	"io"
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
	client     = newClient()
	allowedCT  = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
	pageLimit  = int64(50 << 20) // cap HTML bodies
	imageLimit = int64(50 << 20) // cap image bodies
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
}

// Run crawls domain up to depth link levels and downloads matching images
// into a fresh YYYY-MM-DD-HH folder under out.
func Run(domain string, depth, minW, minH, workers, maxPages, quality int, out string) (Result, error) {
	seed, err := seedURL(domain)
	if err != nil {
		return Result{}, err
	}
	start := time.Now()
	pages, candidates := crawl(seed, depth, workers, maxPages)

	seen := map[string]bool{}
	unique := make([]string, 0, len(candidates))
	for _, u := range candidates {
		if !seen[u] {
			seen[u] = true
			unique = append(unique, u)
		}
	}

	dir, err := pickDir(out)
	if err != nil {
		return Result{}, err
	}
	saved, skipped, recs := downloadAll(unique, dir, filepath.Base(dir), minW, minH, quality, workers)

	return Result{
		SeedHost:   seed.Host,
		Start:      start,
		Duration:   time.Since(start),
		Depth:      depth,
		Pages:      pages,
		Candidates: len(unique),
		Saved:      saved,
		Skipped:    skipped,
		Images:     recs,
		Dir:        dir,
	}, nil
}

// pickDir returns a fresh YYYY-MM-DD-HH folder under out; same-hour reruns
// would overwrite each other, so a -2/-3 suffix is appended as needed.
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
	return &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// get fetches a URL with browser-like headers, retrying 3 times with
// backoff on 403/429/5xx (the usual bot-protection statuses).
func Get(u string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			wait := time.Duration(1<<attempt) * time.Second // 1s, 2s, 4s
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			resp.Body.Close()
			time.Sleep(wait)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// crawl BFSes from seed up to depth link levels. Follows links on the seed
// domain and its subdomains (ifeng.com links to news.ifeng.com, etc), but
// guesses which subdomains are worth entering: known service subdomains are
// skipped entirely, and a fresh subdomain whose first page has no links and
// no images is marked dead and abandoned.
func crawl(seed *url.URL, depth, workers, maxPages int) (pages int, imgs []string) {
	visited := map[string]bool{seed.String(): true}
	seenHost := map[string]bool{}
	deadHosts := map[string]bool{}
	queue := []string{seed.String()}
	var mu sync.Mutex
	for level := 0; level <= depth && len(queue) > 0; level++ {
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
				if deadHosts[h] || pages >= maxPages {
					mu.Unlock()
					return
				}
				mu.Unlock()
				links, pageImgs := FetchPage(u)
				mu.Lock()
				defer mu.Unlock()
				pages++
				imgs = append(imgs, pageImgs...)
				if !seenHost[h] {
					seenHost[h] = true
					// best guess: a content-free first page means a service
					// shell, abandon the whole subdomain
					if len(links) == 0 && len(pageImgs) == 0 {
						deadHosts[h] = true
					}
				}
				for _, l := range links {
					if !deadHosts[h] && !visited[l] {
						visited[l] = true
						next = append(next, l)
					}
				}
			}(u)
		}
		wg.Wait()
		queue = next
	}
	return pages, imgs
}

func hostOf(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		return parsed.Hostname()
	}
	return u
}

// fetchPage returns same-host links and absolute image URLs found on a page.
func FetchPage(u string) (links, imgs []string) {
	resp, err := Get(u)
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
	base := resp.Request.URL // final URL after redirects; links on ifeng.com 302 to www.ifeng.com
	host := strings.TrimPrefix(base.Hostname(), "www.") // match seed domain + subdomains
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "a":
				if abs := resolve(base, attr(n, "href")); abs != nil && sameSite(abs.Hostname(), host) && !serviceSubdomain(abs.Hostname(), host) {
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

func downloadAll(urls []string, dir, relBase string, minW, minH, quality, workers int) (saved, skipped int, recs []harvestdb.Image) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, workers)
	var n atomic.Uint32
	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			img, err := saveImage(u, relBase, dir, n.Add(1), minW, minH, quality)
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
	resp, err := Get(u)
	if err != nil {
		img.Skipped = err.Error()
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
	}
	return img, os.WriteFile(filepath.Join(dir, name), data, 0o644)
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
		if im.Phash != 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(out, im.SavedPath))
		if err != nil {
			continue // file missing; skip
		}
		m, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		if err := store.SetPhash(im.ID, int64(phash.DHash(m))); err != nil {
			return hashed, err
		}
		hashed++
	}
	return hashed, nil
}
