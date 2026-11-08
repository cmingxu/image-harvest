// Package web serves the harvest dashboard API (runs, images, similarity)
// and the harvested media files.
package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"image-harvest/internal/harvestdb"
)

// Opener hands out a store for one request and the matching cleanup func.
//
// DuckDB is a single-writer embedded database: a process holding a read-only
// connection blocks other processes from opening the file read-write, and vice
// versa. Opening per request keeps the dashboard's lock window down to a few
// milliseconds so a crawl run can grab the write lock in between.
type Opener func() (*harvestdb.Store, func(), error)

// pageParams reads ?limit/?offset/?page, clamps them and defaults sensibly.
func pageParams(c *gin.Context, defLimit, maxLimit int) (limit, offset int) {
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(defLimit)))
	if limit <= 0 {
		limit = defLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if p, err := strconv.Atoi(c.Query("page")); err == nil && p > 1 {
		offset = (p - 1) * limit
	}
	if o, err := strconv.Atoi(c.Query("offset")); err == nil && o > 0 {
		offset = o
	}
	return limit, offset
}

// withStore wraps a handler so it runs with a fresh store and always releases it.
func withStore(open Opener, h func(*gin.Context, *harvestdb.Store)) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, done, err := open()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database unavailable: " + err.Error()})
			return
		}
		defer done()
		h(c, store)
	}
}

// Meta is the runtime configuration and dataset summary shown on the dashboard.
type Meta struct {
	DBPath    string            `json:"db_path"`
	DBSize    int64             `json:"db_size_bytes"`
	MediaRoot string            `json:"media_root"`
	Config    map[string]string `json:"config"`
	Stats     harvestdb.Stats   `json:"stats"`
}

// NewRouter builds the API + media routes on top of the DuckDB store.
func NewRouter(open Opener, out string, meta func() Meta) *gin.Engine {
	r := gin.Default()
	api := r.Group("/api")

	api.GET("/runs", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		limit, offset := pageParams(c, 50, 500)
		runs, err := store.ListRunsPage(limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		total, err := store.CountRuns()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs, "total": total, "limit": limit, "offset": offset})
	}))

	api.GET("/runs/:id", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad run id"})
			return
		}
		run, err := store.GetRun(id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"run": run})
	}))

	api.GET("/runs/:id/images", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad run id"})
			return
		}
		filter := c.DefaultQuery("skipped", "saved")
		if filter != "saved" && filter != "skipped" && filter != "all" {
			filter = "saved"
		}
		limit, offset := pageParams(c, 60, 500)
		imgs, err := store.ListImagesPage(id, filter, limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		total, err := store.CountImages(id, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"images": imgs, "total": total, "limit": limit, "offset": offset})
	}))

	api.GET("/images/:id/similar", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad image id"})
			return
		}
		img, err := store.GetImage(id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
			return
		}
		if img.Phash == 0 {
			c.JSON(http.StatusOK, gin.H{"images": []harvestdb.Similar{}})
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "12"))
		maxDist, _ := strconv.Atoi(c.DefaultQuery("distance", "10")) // 10/64 bits ≈ visually same
		scope := c.DefaultQuery("scope", "all")
		sims, err := store.SimilarImages(*img, maxDist, limit, scope)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"images": sims})
	}))

	api.GET("/domains", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		limit, offset := pageParams(c, 50, 500)
		domains, err := store.ListDomainsPage(limit, offset, c.DefaultQuery("sort", "id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		total, err := store.CountDomains()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"domains": domains, "total": total, "limit": limit, "offset": offset})
	}))

	// metadata: dataset stats + the crawl configuration in force
	api.GET("/meta", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		m := meta()
		st, err := store.Stats()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		m.Stats = st
		c.JSON(http.StatusOK, m)
	}))

	api.GET("/categories", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		cats, err := store.ListCategories()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"categories": cats})
	}))

	// serve harvested image files from the output directory, traversal-safe
	r.GET("/media/*path", func(c *gin.Context) {
		absOut, err := filepath.Abs(out)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		p := filepath.Join(absOut, filepath.FromSlash(c.Param("path")))
		absP, err := filepath.Abs(p)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		if absP != absOut && !strings.HasPrefix(absP, absOut+string(os.PathSeparator)) {
			c.Status(http.StatusForbidden)
			return
		}
		c.File(absP)
	})

	return r
}
