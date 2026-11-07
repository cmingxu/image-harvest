package crawler

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

//go:embed testdata/webp1.webp
var webpFixture []byte // 550x368

func jpegImg(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillPattern(img) // non-solid so dHash != 0
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngImg(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillPattern(img)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fillPattern(img *image.RGBA) {
	b := img.Bounds()
	mx, my := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.RGBA{A: 255}
			if (x < mx && y < my) || (x >= mx && y >= my) {
				c.R = 255
			}
			img.Set(x, y, c)
		}
	}
}

// TestHarvest: seed page has a big jpg (saved), a small png (skipped, below
// min-res), a flaky jpg (429 then succeeds), and a link to page2 whose webp
// passes min-res (converted to jpg). Expect 3 saved .jpg files, and every
// saved record carries a perceptual hash.
func TestHarvest(t *testing.T) {
	var flaky atomic.Int32
	mux2 := http.NewServeMux() // the "www" host: redirect target + content
	mux2.HandleFunc("/home", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><img src="/big.jpg"><img src="/small.png"><img src="/flaky.jpg"><a href="/page2">2</a></html>`)
	})
	mux2.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><img src="/pic.webp"></html>`)
	})
	mux2.HandleFunc("/big.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write(jpegImg(t, 400, 300)) })
	mux2.HandleFunc("/flaky.jpg", func(w http.ResponseWriter, r *http.Request) {
		if flaky.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Write(jpegImg(t, 250, 250))
	})
	mux2.HandleFunc("/small.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pngImg(t, 100, 100)) })
	mux2.HandleFunc("/pic.webp", func(w http.ResponseWriter, r *http.Request) { w.Write(webpFixture) })
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()

	mux := http.NewServeMux() // seed host, only a redirect like ifeng.com -> www
	mux.HandleFunc("/seed", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv2.URL+"/home", http.StatusMovedPermanently)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	res, err := Run(srv.URL+"/seed", 1, 200, 200, 4, 500, 85, dir)
	if err != nil {
		t.Fatal(err)
	}

	if res.Pages != 2 {
		t.Errorf("want 2 pages crawled, got %d", res.Pages)
	}
	if res.Candidates != 4 {
		t.Errorf("want 4 candidates, got %d", res.Candidates)
	}
	if res.Saved != 3 || res.Skipped != 1 {
		t.Errorf("want 3 saved / 1 skipped, got %d / %d", res.Saved, res.Skipped)
	}
	if len(res.Images) != 4 {
		t.Fatalf("want 4 image records, got %d", len(res.Images))
	}
	hashed := 0
	for _, im := range res.Images {
		if im.Skipped == "" && im.Phash == 0 {
			t.Errorf("saved image %s has no perceptual hash", im.SavedPath)
		}
		if im.Skipped == "" {
			hashed++
		}
	}
	if hashed != 3 {
		t.Errorf("want 3 hashed saved images, got %d", hashed)
	}

	var files []string
	outEntries, err := os.ReadDir(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range outEntries {
		files = append(files, e.Name())
	}
	if len(files) != 3 {
		t.Fatalf("want 3 saved files, got %v", files)
	}
	for _, f := range files {
		if filepath.Ext(f) != ".jpg" {
			t.Errorf("expected .jpg, got %s", f)
		}
	}
}

func TestSameSite(t *testing.T) {
	cases := []struct{ host, seed string; want bool }{
		{"www.ifeng.com", "ifeng.com", true},
		{"news.ifeng.com", "ifeng.com", true},
		{"ifeng.com", "ifeng.com", true},
		{"fengcx.com", "ifeng.com", false},
		{"evilifeng.com", "ifeng.com", false},
		{"ifeng.com.evil.com", "ifeng.com", false},
	}
	for _, c := range cases {
		if got := sameSite(c.host, c.seed); got != c.want {
			t.Errorf("sameSite(%q, %q) = %v, want %v", c.host, c.seed, got, c.want)
		}
	}
}

func TestServiceSubdomain(t *testing.T) {
	cases := []struct{ host, seed string; want bool }{
		{"mail.ifeng.com", "ifeng.com", true},
		{"api.ifeng.com", "ifeng.com", true},
		{"news.ifeng.com", "ifeng.com", false},
		{"cdn.ifeng.com", "ifeng.com", false},
		{"mail.staff.ifeng.com", "ifeng.com", true}, // first label decides
		{"ifeng.com", "ifeng.com", false},
	}
	for _, c := range cases {
		if got := serviceSubdomain(c.host, c.seed); got != c.want {
			t.Errorf("serviceSubdomain(%q, %q) = %v, want %v", c.host, c.seed, got, c.want)
		}
	}
}

// TestSubdomainProbe: seed links to mail.* (blocked outright by blocklist),
// cdn.* (content, crawled), and blog.* whose first page is empty (probe marks
// the host dead). blog.* also has /other, discovered only via cdn's page, so
// it lands at level 2 — after the probe ran — and is never fetched.
// Uses *.localhost -> 127.0.0.1 to fake subdomains.
func TestSubdomainProbe(t *testing.T) {
	var portB string
	muxB := http.NewServeMux()
	muxB.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><form><input name="pw"></form></html>`)
	})
	muxB.HandleFunc("/gallery", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><img src="/p.png"><a href="http://blog.localhost:%s/other">x</a></html>`, portB)
	})
	muxB.HandleFunc("/p.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pngImg(t, 300, 300)) })
	muxB.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html></html>`) // content-free: marks blog.localhost dead
	})
	muxB.HandleFunc("/other", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><img src="/p.png"></html>`) // must never be reached
	})
	srvB := httptest.NewUnstartedServer(muxB)
	lB, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	srvB.Listener = lB
	srvB.Start()
	defer srvB.Close()
	_, portB, _ = net.SplitHostPort(lB.Addr().String())

	muxA := http.NewServeMux()
	muxA.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><img src="/big.jpg"><a href="http://mail.localhost:%s/login">mail</a><a href="http://cdn.localhost:%s/gallery">cdn</a><a href="http://blog.localhost:%s/empty">blog</a></html>`, portB, portB, portB)
	})
	muxA.HandleFunc("/big.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write(jpegImg(t, 400, 300)) })
	srvA := httptest.NewUnstartedServer(muxA)
	lA, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	srvA.Listener = lA
	srvA.Start()
	defer srvA.Close()
	_, portA, _ := net.SplitHostPort(lA.Addr().String())
	srvA.URL = "http://localhost:" + portA // name the seed host "localhost" so *.localhost links match

	dir := t.TempDir()
	res, err := Run(srvA.URL, 2, 100, 100, 4, 500, 85, dir)
	if err != nil {
		t.Fatal(err)
	}
	// big.jpg + p.png; mail/login never crawled, blog/another never crawled
	if res.Saved != 2 {
		t.Fatalf("want 2 saved images, got %d", res.Saved)
	}
}
