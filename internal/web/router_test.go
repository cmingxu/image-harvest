package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"image-harvest/internal/harvestdb"
)

func TestRouter(t *testing.T) {
	dir := t.TempDir()
	store, err := harvestdb.Open(filepath.Join(dir, "h.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	runID, err := store.InsertRun(harvestdb.Run{
		Domain: "example.com", StartedAt: "2026-11-05 10:00:00",
		DurationS: 1.5, Depth: 2, PagesVisited: 10, ImagesDownloaded: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertImages(runID, []harvestdb.Image{
		{URL: "http://example.com/a.jpg", Width: 100, Height: 100, Ext: ".jpg", SavedPath: "2026-11-05-10/001.jpg", SizeBytes: 123, Phash: 1},
		{URL: "http://example.com/b.png", Skipped: "10x10 below minimum"},
		{URL: "http://example.com/near.jpg", SavedPath: "2026-11-05-10/002.jpg", Phash: 2},
		{URL: "http://example.com/far.jpg", SavedPath: "2026-11-05-10/003.jpg", Phash: -1},
	}); err != nil {
		t.Fatal(err)
	}

	media := filepath.Join(dir, "2026-11-05-10")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "001.jpg"), []byte("fakeimage"), 0o644); err != nil {
		t.Fatal(err)
	}

	// the real server opens a short-lived read-only connection per request;
	// the test hands out the shared store instead.
	r := NewRouter(Opener(func() (*harvestdb.Store, func(), error) {
		return store, func() {}, nil
	}), dir, func() Meta {
		return Meta{DBPath: "h.duckdb", MediaRoot: dir, Config: map[string]string{"depth": "2"}}
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	getJSON := func(path string) map[string]json.RawMessage {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		var m map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	var runs []harvestdb.Run
	if err := json.Unmarshal(getJSON("/api/runs")["runs"], &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Domain != "example.com" || runs[0].ImagesDownloaded != 2 {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	var saved, skipped []harvestdb.Image
	if err := json.Unmarshal(getJSON("/api/runs/1/images?skipped=saved")["images"], &saved); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(getJSON("/api/runs/1/images?skipped=skipped")["images"], &skipped); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 || saved[0].SavedPath != "2026-11-05-10/001.jpg" {
		t.Fatalf("unexpected saved images: %+v", saved)
	}
	if len(skipped) != 1 || skipped[0].Skipped == "" {
		t.Fatalf("unexpected skipped images: %+v", skipped)
	}

	// similarity: image 1 (phash=1) matches image 3 (phash=2, dist 2), not 4
	var sims []harvestdb.Similar
	if err := json.Unmarshal(getJSON("/api/images/1/similar")["images"], &sims); err != nil {
		t.Fatal(err)
	}
	if len(sims) != 1 || sims[0].SavedPath != "2026-11-05-10/002.jpg" || sims[0].Distance != 2 {
		t.Fatalf("unexpected similar images: %+v", sims)
	}

	// media file served, traversal blocked
	resp, err := http.Get(srv.URL + "/media/2026-11-05-10/001.jpg")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "fakeimage" {
		t.Fatalf("media body = %q", body)
	}

	resp, err = http.Get(srv.URL + "/media/../h.duckdb")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("traversal: want 403, got %d", resp.StatusCode)
	}
}
