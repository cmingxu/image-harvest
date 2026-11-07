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

// NewRouter builds the API + media routes on top of the DuckDB store.
func NewRouter(store *harvestdb.Store, out string) *gin.Engine {
	r := gin.Default()
	api := r.Group("/api")

	api.GET("/runs", func(c *gin.Context) {
		runs, err := store.ListRuns()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	})

	api.GET("/runs/:id", func(c *gin.Context) {
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
	})

	api.GET("/runs/:id/images", func(c *gin.Context) {
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
	})

	api.GET("/images/:id/similar", func(c *gin.Context) {
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
	})

	api.GET("/domains", func(c *gin.Context) {
		domains, err := store.ListDomains()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"domains": domains})
	})

	api.GET("/categories", func(c *gin.Context) {
		cats, err := store.ListCategories()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"categories": cats})
	})

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
