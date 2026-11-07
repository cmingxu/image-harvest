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

// NewRouter builds the API + media routes on top of the DuckDB store.
func NewRouter(open Opener, out string) *gin.Engine {
	r := gin.Default()
	api := r.Group("/api")

	api.GET("/runs", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		runs, err := store.ListRuns()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
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
		imgs, err := store.ListImages(id, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"images": imgs})
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
		maxDist, _ := strconv.Atoi(c.DefaultQuery("distance", "25"))
		sims, err := store.SimilarImages(img.ID, img.Phash, img.RunID, maxDist, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"images": sims})
	}))

	api.GET("/domains", withStore(open, func(c *gin.Context, store *harvestdb.Store) {
		domains, err := store.ListDomains()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"domains": domains})
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
