// Package discover probes candidate domains for likely high-resolution image
// yield: it fetches each homepage, counts image URLs (density), samples a few
// images and decodes their dimensions (hi-res ratio), and scores the domain.
package discover

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"image-harvest/internal/harvestdb"
)

const (
	imageSampleCap = 3
	minHiResW      = 800
	minHiResH      = 600
	// probe is a cheap measurement: no retries, short timeout (crawl-grade
	// retry/backoff would take hours on 2000 mostly-blocked domains)
	probeTimeout = 8 * time.Second
	ua           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// DefaultList is the bulk candidate source: Cisco Umbrella top-1M CSV.
const DefaultList = "https://s3-us-west-1.amazonaws.com/umbrella-static/top-1m.csv.zip"

var probeClient = &http.Client{Timeout: probeTimeout}

// LoadList returns candidate domains from a local file path or URL.
// For a URL, the payload is cached at cachePath (unless refresh is true) so
// repeated runs don't re-download. Supports .zip (Umbrella) and plain
// CSV/one-domain-per-line files.
func LoadList(pathOrURL, cachePath string, refresh bool) ([]string, error) {
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		if !refresh {
			if data, err := os.ReadFile(cachePath); err == nil {
				return parseList(data), nil
			}
		}
		fmt.Printf("downloading %s ...\n", pathOrURL)
		resp, err := http.Get(pathOrURL)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(pathOrURL, ".zip") {
			if data, err = unzipFirst(data); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(cachePath, data, 0o644); err != nil {
			return nil, err
		}
		return parseList(data), nil
	}
	data, err := os.ReadFile(pathOrURL)
	if err != nil {
		return nil, err
	}
	return parseList(data), nil
}

// parseList handles "rank,domain[,tld]" CSVs and plain domain-per-line lists.
func parseList(data []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		dom := strings.TrimSpace(fields[len(fields)-1]) // domain is the last column
		if dom == "" || strings.ContainsAny(dom, " /") || seen[dom] {
			continue
		}
		seen[dom] = true
		out = append(out, dom)
	}
	return out
}

func unzipFirst(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 200<<20))
		}
	}
	return nil, fmt.Errorf("empty zip")
}

// Run probes list[offset:offset+limit] with workers goroutines, upserts the
// results, and returns all probed domains ranked by score.
func Run(store *harvestdb.Store, list []string, workers, limit, offset int) ([]harvestdb.Domain, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || offset >= len(list) {
		return store.ListDomains()
	}
	end := min(offset+limit, len(list))
	cands := list[offset:end]

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	probed := make([]harvestdb.Domain, 0, len(cands))
	for _, d := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(d string) {
			defer wg.Done()
			defer func() { <-sem }()
			dom := probe(d)
			dom.Source = "top-1m"
			dom.LastChecked = time.Now().Format("2006-01-02 15:04:05")
			mu.Lock()
			probed = append(probed, dom)
			// upserts are serialized too: concurrent MAX(id)+1 inserts race
			_, err := store.UpsertDomain(dom)
			mu.Unlock()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: upsert %s: %v\n", dom.Domain, err)
			}
		}(d)
	}
	wg.Wait()
	scored := 0
	for _, d := range probed {
		if d.Score > 0 {
			scored++
		}
	}
	fmt.Printf("probed %d candidates (%d scored)\n", len(cands), scored)
	return store.ListDomains()
}

// probe measures one domain. Score = density × (1 + hi-res ratio): rewards
// both image volume and resolution. Probe failures are recorded, not fatal.
func probe(domain string) harvestdb.Domain {
	d := harvestdb.Domain{Domain: domain}
	seed := domain
	if !strings.HasPrefix(seed, "http://") && !strings.HasPrefix(seed, "https://") {
		seed = "https://" + seed
	}
	resp, err := probeFetch(seed)
	if err != nil {
		d.Note = "fetch error: " + err.Error()
		return d
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		d.Note = fmt.Sprintf("status %d", resp.StatusCode)
		return d
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		d.Note = "parse error"
		return d
	}
	base := resp.Request.URL
	seen := map[string]bool{}
	var imgs []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" {
			for _, src := range []string{attr(n, "src"), firstSrcset(n)} {
				if src == "" {
					continue
				}
				if abs, err := base.Parse(src); err == nil && (abs.Scheme == "http" || abs.Scheme == "https") {
					u := abs.String()
					if !seen[u] {
						seen[u] = true
						imgs = append(imgs, u)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(imgs) == 0 {
		d.Note = "no images on homepage"
		return d
	}
	if len(imgs) > 30 {
		imgs = imgs[:30] // density is capped; enough to judge
	}
	d.ImgDensity = len(imgs)

	hiRes := 0
	sampled := 0
	for _, u := range imgs[:min(len(imgs), imageSampleCap)] {
		body, err := fetchImage(u)
		if err != nil {
			continue
		}
		sampled++
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err == nil && cfg.Width >= minHiResW && cfg.Height >= minHiResH {
			hiRes++
		}
	}
	d.Samples = sampled
	if sampled > 0 {
		d.HiResRatio = float64(hiRes) / float64(sampled)
	}
	d.Score = float64(d.ImgDensity) * (1 + d.HiResRatio)
	if d.Score == 0 {
		d.Note = "samples undecodable"
	}
	return d
}

func probeFetch(u string) (*http.Response, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	return probeClient.Do(req)
}

func fetchImage(u string) ([]byte, error) {
	resp, err := probeFetch(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func firstSrcset(n *html.Node) string {
	for _, cand := range strings.Split(attr(n, "srcset"), ",") {
		if f := strings.Fields(cand); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}
