package harvestdb

import (
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "h.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	runID, err := s.InsertRun(Run{
		Domain: "example.com", StartedAt: "2026-11-05 10:00:00",
		DurationS: 2.5, Depth: 2, PagesVisited: 10, ImagesDownloaded: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runID != 1 {
		t.Fatalf("want run id 1, got %d", runID)
	}
	if err := s.InsertImages(runID, []Image{
		{URL: "http://example.com/a.jpg", Width: 100, Height: 200, Ext: ".jpg", SavedPath: "2026-11-05-10/001.jpg", SizeBytes: 123, Phash: 1},
		{URL: "http://example.com/b.png", Skipped: "10x10 below minimum", Phash: 0},
	}); err != nil {
		t.Fatal(err)
	}

	runs, err := s.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Domain != "example.com" || runs[0].ImagesDownloaded != 2 {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	imgs, err := s.ListImages(runID, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 || imgs[0].Phash != 1 {
		t.Fatalf("unexpected images: %+v", imgs)
	}
	saved, err := s.ListImages(runID, "saved")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Skipped != "" {
		t.Fatalf("unexpected saved: %+v", saved)
	}
}

func TestSimilarImages(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "h.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	runID, err := s.InsertRun(Run{Domain: "x.com", StartedAt: "t"})
	if err != nil {
		t.Fatal(err)
	}
	// target 0b...1 ; near = 0b...10 (dist 2); far = all-ones (dist 63)
	if err := s.InsertImages(runID, []Image{
		{URL: "t", SavedPath: "r/001.jpg", Phash: 1},
		{URL: "near", SavedPath: "r/002.jpg", Phash: 2},
		{URL: "far", SavedPath: "r/003.jpg", Phash: -1},
	}); err != nil {
		t.Fatal(err)
	}

	sims, err := s.SimilarImages(1, 1, runID, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sims) != 1 || sims[0].URL != "near" || sims[0].Distance != 2 {
		t.Fatalf("unexpected similar: %+v", sims)
	}

	// no match at distance 0 except the excluded target itself
	sims, err = s.SimilarImages(2, 2, runID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sims) != 0 {
		t.Fatalf("target itself must be excluded, got %+v", sims)
	}
}

func TestCategoriesRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.ReplaceCategories("design", []CategoryNode{
		{Name: "动漫动画", URL: "https://www.nipic.com/design/acg/index.html", Children: []CategoryNode{
			{Name: "动漫人物", URL: "https://www.nipic.com/design/acg/renwu/index.html"},
		}},
		{Name: "平面设计", URL: "https://www.nipic.com/design/pingmian/index.html"},
	}); err != nil {
		t.Fatal(err)
	}

	cats, err := s.ListCategories()
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(cats), cats)
	}
	// roots first: 动漫动画(0), 平面设计(0), then 动漫人物(parent=动漫动画's id)
	if cats[0].ParentID != 0 || cats[1].ParentID != 0 || cats[2].ParentID != cats[0].ID {
		t.Fatalf("unexpected parent links: %+v", cats)
	}

	// refresh replaces the kind
	if err := s.ReplaceCategories("design", []CategoryNode{{Name: "仅此一个", URL: "u"}}); err != nil {
		t.Fatal(err)
	}
	cats, err = s.ListCategories()
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 1 || cats[0].Name != "仅此一个" {
		t.Fatalf("refresh should replace, got %+v", cats)
	}
}
