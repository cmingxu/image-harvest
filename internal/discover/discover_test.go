package discover

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"image-harvest/internal/harvestdb"
)

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseList(t *testing.T) {
	data := []byte("1,google.com\n2, example.com \n3,bad url.com\n4,google.com\n\nduckduckgo.com\n")
	got := parseList(data)
	want := []string{"google.com", "example.com", "duckduckgo.com"}
	if len(got) != len(want) {
		t.Fatalf("parseList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseList = %v, want %v", got, want)
		}
	}
}

// TestRunScoring: a homepage dense with hi-res images must score higher than
// a sparse homepage with small images, and the store must rank it first.
func TestRunScoring(t *testing.T) {
	big := jpegOf(t, 1000, 800)
	small := jpegOf(t, 100, 100)

	muxDense := http.NewServeMux()
	muxDense.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>`+
			`<img src="/a.jpg"><img src="/b.jpg"><img src="/c.jpg"><img src="/d.jpg"><img src="/e.jpg">`+
			`<img srcset="/f.jpg 1x, /f2.jpg 2x">`+`</html>`)
	})
	for _, p := range []string{"/a.jpg", "/b.jpg", "/c.jpg", "/d.jpg", "/e.jpg", "/f.jpg", "/f2.jpg"} {
		muxDense.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.Write(big) })
	}
	srvD := httptest.NewServer(muxDense)
	defer srvD.Close()

	muxSparse := http.NewServeMux()
	muxSparse.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><img src="/tiny.jpg"></html>`)
	})
	muxSparse.HandleFunc("/tiny.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write(small) })
	srvS := httptest.NewServer(muxSparse)
	defer srvS.Close()

	store, err := harvestdb.Open(filepath.Join(t.TempDir(), "d.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// probe dense, sparse, and one unreachable domain from a "top list"
	sink := func(batch []harvestdb.Domain) error {
		for _, d := range batch {
			if _, err := store.UpsertDomain(d); err != nil {
				return err
			}
		}
		return nil
	}
	domains, err := Run(sink, []string{srvD.URL, srvS.URL, "http://127.0.0.1:1"},
		Options{Workers: 2, Limit: 10, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 3 {
		t.Fatalf("want 3 probed domains, got %d: %+v", len(domains), domains)
	}
	if domains[0].Domain != srvD.URL {
		t.Fatalf("want dense site ranked first, got %+v", domains[0])
	}

	// only reachable domains become candidates in the database
	stored, err := store.ListDomains()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("want 2 stored candidates (unreachable dropped), got %d: %+v", len(stored), stored)
	}
	for _, d := range stored {
		if d.Domain == "http://127.0.0.1:1" {
			t.Fatalf("unreachable domain must not be stored: %+v", d)
		}
	}
}

func TestLoadListFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "list.csv")
	if err := os.WriteFile(p, []byte("1,aaa.com\n2,bbb.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadList(p, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "aaa.com" || got[1] != "bbb.com" {
		t.Fatalf("LoadList = %v", got)
	}
}
