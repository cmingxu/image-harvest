// Package harvestdb persists crawl metadata (runs + images) in DuckDB.
package harvestdb

import (
	"database/sql"
	"sort"

	_ "github.com/marcboeker/go-duckdb" // registers the "duckdb" driver

	"image-harvest/internal/phash"
)

// Run is one crawl invocation.
type Run struct {
	ID               int64   `json:"id"`
	Domain           string  `json:"domain"`
	StartedAt        string  `json:"started_at"`
	DurationS        float64 `json:"duration_s"`
	Depth            int     `json:"depth"`
	PagesVisited     int     `json:"pages_visited"`
	ImagesDownloaded int     `json:"images_downloaded"`
}

// Image is one image candidate of a run. Skipped == "" means it was saved;
// otherwise it holds the skip reason (below minimum, bad type, decode error).
// Phash is the 64-bit perceptual hash of the image (0 = not hashed).
type Image struct {
	ID        int64  `json:"id"`
	RunID     int64  `json:"run_id"`
	URL       string `json:"url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Ext       string `json:"ext"`
	SavedPath string `json:"saved_path"`
	SizeBytes int64  `json:"size_bytes"`
	Skipped   string `json:"skipped"`
	Phash     int64  `json:"-"`
}

// Similar is an Image paired with its perceptual distance to a query image.
type Similar struct {
	Image
	Distance int `json:"distance"`
}

// Domain is one probed candidate domain from discovery.
type Domain struct {
	ID          int64   `json:"id"`
	Domain      string  `json:"domain"`
	Source      string  `json:"source"`
	Score       float64 `json:"score"`
	ImgDensity  int     `json:"img_density"`
	HiResRatio  float64 `json:"hi_res_ratio"`
	Samples     int     `json:"samples"`
	LastChecked string  `json:"last_checked"`
	Note        string  `json:"note"`
}

// CategoryNode is one node of a category tree (a top-level category with
// its children, or a single child).
type CategoryNode struct {
	Name     string         `json:"name"`
	URL      string         `json:"url"`
	Children []CategoryNode `json:"children,omitempty"`
}

// Category is one flattened category row.
type Category struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	ParentID int64  `json:"parent_id"`
}

const schema = `
CREATE TABLE IF NOT EXISTS runs (
  id INTEGER PRIMARY KEY,
  domain TEXT NOT NULL,
  started_at TEXT NOT NULL,
  duration_s DOUBLE,
  depth INTEGER,
  pages_visited INTEGER,
  images_downloaded INTEGER
);
CREATE TABLE IF NOT EXISTS images (
  id INTEGER PRIMARY KEY,
  run_id INTEGER,
  url TEXT,
  width INTEGER DEFAULT 0,
  height INTEGER DEFAULT 0,
  ext TEXT,
  saved_path TEXT,
  size_bytes INTEGER DEFAULT 0,
  skipped TEXT DEFAULT '',
  phash BIGINT DEFAULT 0
);
CREATE TABLE IF NOT EXISTS domains (
  id INTEGER PRIMARY KEY,
  domain TEXT,
  source TEXT,
  score DOUBLE DEFAULT 0,
  img_density INTEGER DEFAULT 0,
  hi_res_ratio DOUBLE DEFAULT 0,
  samples INTEGER DEFAULT 0,
  last_checked TEXT,
  note TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS categories (
  id INTEGER PRIMARY KEY,
  kind TEXT,
  name TEXT,
  url TEXT,
  parent_id INTEGER DEFAULT 0
);`

// Store wraps the DuckDB file.
type Store struct{ db *sql.DB }

// Open opens path (creating it if needed) and ensures the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// migration for databases created before the phash column existed
	if _, err := db.Exec("ALTER TABLE images ADD COLUMN IF NOT EXISTS phash BIGINT DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// InsertRun appends a run row and returns its id. DuckDB has no auto-increment
// for plain INTEGER PRIMARY KEY, so ids come from MAX(id)+1.
func (s *Store) InsertRun(r Run) (int64, error) {
	var id int64
	if err := s.db.QueryRow("SELECT COALESCE(MAX(id),0)+1 FROM runs").Scan(&id); err != nil {
		return 0, err
	}
	_, err := s.db.Exec(`INSERT INTO runs (id, domain, started_at, duration_s, depth, pages_visited, images_downloaded)
		VALUES (?,?,?,?,?,?,?)`,
		id, r.Domain, r.StartedAt, r.DurationS, r.Depth, r.PagesVisited, r.ImagesDownloaded)
	return id, err
}

// InsertImages stores image rows for one run in a single transaction.
func (s *Store) InsertImages(runID int64, imgs []Image) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, im := range imgs {
		var id int64
		if err := tx.QueryRow("SELECT COALESCE(MAX(id),0)+1 FROM images").Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO images (id, run_id, url, width, height, ext, saved_path, size_bytes, skipped, phash)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, runID, im.URL, im.Width, im.Height, im.Ext, im.SavedPath, im.SizeBytes, im.Skipped, im.Phash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListRuns returns all runs, newest first.
func (s *Store) ListRuns() ([]Run, error) {
	rows, err := s.db.Query("SELECT id, domain, started_at, duration_s, depth, pages_visited, images_downloaded FROM runs ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Domain, &r.StartedAt, &r.DurationS, &r.Depth, &r.PagesVisited, &r.ImagesDownloaded); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// GetRun returns one run by id.
func (s *Store) GetRun(id int64) (*Run, error) {
	var r Run
	err := s.db.QueryRow("SELECT id, domain, started_at, duration_s, depth, pages_visited, images_downloaded FROM runs WHERE id = ?", id).
		Scan(&r.ID, &r.Domain, &r.StartedAt, &r.DurationS, &r.Depth, &r.PagesVisited, &r.ImagesDownloaded)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpsertDomain inserts a fresh probe result for domain, replacing any older
// row for the same domain.
func (s *Store) UpsertDomain(d Domain) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM domains WHERE domain = ?", d.Domain).Scan(&id)
	switch {
	case err == nil:
		_, err = s.db.Exec(`UPDATE domains SET source=?, score=?, img_density=?, hi_res_ratio=?, samples=?, last_checked=?, note=? WHERE id=?`,
			d.Source, d.Score, d.ImgDensity, d.HiResRatio, d.Samples, d.LastChecked, d.Note, id)
	case err == sql.ErrNoRows:
		if err := s.db.QueryRow("SELECT COALESCE(MAX(id),0)+1 FROM domains").Scan(&id); err != nil {
			return 0, err
		}
		_, err = s.db.Exec(`INSERT INTO domains (id, domain, source, score, img_density, hi_res_ratio, samples, last_checked, note)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			id, d.Domain, d.Source, d.Score, d.ImgDensity, d.HiResRatio, d.Samples, d.LastChecked, d.Note)
	default:
		return 0, err
	}
	return id, err
}

// ReplaceCategories rebuilds the tree for one kind: every existing row of
// that kind is removed and the roots (with their children) are stored flat.
func (s *Store) ReplaceCategories(kind string, roots []CategoryNode) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// allocate ids before deleting: DuckDB's PK index keeps tombstones for
	// rows deleted in the same transaction, so reused ids would collide
	var nextID int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(id),0)+1 FROM categories").Scan(&nextID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM categories WHERE kind = ?", kind); err != nil {
		return err
	}
	var rootIDs []int64
	for _, r := range roots {
		if _, err := tx.Exec(`INSERT INTO categories (id, kind, name, url, parent_id) VALUES (?,?,?,?,0)`, nextID, kind, r.Name, r.URL); err != nil {
			return err
		}
		rootIDs = append(rootIDs, nextID)
		nextID++
		for _, c := range r.Children {
			if _, err := tx.Exec(`INSERT INTO categories (id, kind, name, url, parent_id) VALUES (?,?,?,?,?)`, nextID, kind, c.Name, c.URL, rootIDs[len(rootIDs)-1]); err != nil {
				return err
		}
			nextID++
		}
	}
	return tx.Commit()
}

// ListCategories returns all category rows, roots first.
func (s *Store) ListCategories() ([]Category, error) {
	rows, err := s.db.Query("SELECT id, kind, name, url, parent_id FROM categories ORDER BY parent_id, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.URL, &c.ParentID); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}
func (s *Store) ListDomains() ([]Domain, error) {
	rows, err := s.db.Query("SELECT id, domain, source, score, img_density, hi_res_ratio, samples, last_checked, note FROM domains ORDER BY score DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ds []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.Domain, &d.Source, &d.Score, &d.ImgDensity, &d.HiResRatio, &d.Samples, &d.LastChecked, &d.Note); err != nil {
			return nil, err
		}
		ds = append(ds, d)
	}
	return ds, rows.Err()
}
func (s *Store) ListImages(runID int64, filter string) ([]Image, error) {
	q := "SELECT id, run_id, url, width, height, ext, saved_path, size_bytes, skipped, phash FROM images WHERE run_id = ?"
	switch filter {
	case "saved":
		q += " AND skipped = ''"
	case "skipped":
		q += " AND skipped <> ''"
	}
	q += " ORDER BY id"
	rows, err := s.db.Query(q, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var imgs []Image
	for rows.Next() {
		var im Image
		if err := rows.Scan(&im.ID, &im.RunID, &im.URL, &im.Width, &im.Height, &im.Ext, &im.SavedPath, &im.SizeBytes, &im.Skipped, &im.Phash); err != nil {
			return nil, err
		}
		imgs = append(imgs, im)
	}
	return imgs, rows.Err()
}

// GetImage returns one image by id.
func (s *Store) GetImage(id int64) (*Image, error) {
	var im Image
	err := s.db.QueryRow("SELECT id, run_id, url, width, height, ext, saved_path, size_bytes, skipped, phash FROM images WHERE id = ?", id).
		Scan(&im.ID, &im.RunID, &im.URL, &im.Width, &im.Height, &im.Ext, &im.SavedPath, &im.SizeBytes, &im.Skipped, &im.Phash)
	if err != nil {
		return nil, err
	}
	return &im, nil
}

// SavedImages returns all saved images (used by the phash backfill).
func (s *Store) SavedImages() ([]Image, error) {
	return s.queryImages("SELECT id, run_id, url, width, height, ext, saved_path, size_bytes, skipped, phash FROM images WHERE skipped = '' ORDER BY id")
}

// SetPhash stores the perceptual hash of image id (0 clears it).
func (s *Store) SetPhash(id, phashVal int64) error {
	_, err := s.db.Exec("UPDATE images SET phash = ? WHERE id = ?", phashVal, id)
	return err
}

// SimilarImages returns saved images whose perceptual hash is within maxDist
// bits of phashVal, closest first. runID > 0 restricts the search to one run.
// The query image itself is excluded.
func (s *Store) SimilarImages(targetID, phashVal, runID int64, maxDist, limit int) ([]Similar, error) {
	q := "SELECT id, run_id, url, width, height, ext, saved_path, size_bytes, skipped, phash FROM images WHERE phash <> 0 AND skipped = '' AND id <> ?"
	var args []any
	args = append(args, targetID)
	if runID > 0 {
		q += " AND run_id = ?"
		args = append(args, runID)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	target := uint64(phashVal)
	var sims []Similar
	for rows.Next() {
		var im Image
		if err := rows.Scan(&im.ID, &im.RunID, &im.URL, &im.Width, &im.Height, &im.Ext, &im.SavedPath, &im.SizeBytes, &im.Skipped, &im.Phash); err != nil {
			return nil, err
		}
		d := phash.Hamming(target, uint64(im.Phash))
		if d <= maxDist {
			sims = append(sims, Similar{Image: im, Distance: d})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(sims, func(i, j int) bool { return sims[i].Distance < sims[j].Distance })
	if len(sims) > limit {
		sims = sims[:limit]
	}
	return sims, nil
}

func (s *Store) queryImages(q string, args ...any) ([]Image, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var imgs []Image
	for rows.Next() {
		var im Image
		if err := rows.Scan(&im.ID, &im.RunID, &im.URL, &im.Width, &im.Height, &im.Ext, &im.SavedPath, &im.SizeBytes, &im.Skipped, &im.Phash); err != nil {
			return nil, err
		}
		imgs = append(imgs, im)
	}
	return imgs, rows.Err()
}
