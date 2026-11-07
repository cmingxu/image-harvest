// Package nipic extracts the category tree from nipic.com's sidebar
// (design and photo galleries) — a ready-made taxonomy for organizing
// candidate domains and images.
package nipic

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"image-harvest/internal/crawler"
	"image-harvest/internal/harvestdb"
)

// Pages are the two sidebar sources, keyed by category kind.
var Pages = map[string]string{
	"design": "https://www.nipic.com/design/",
	"photo":  "https://www.nipic.com/photo",
}

// Fetch downloads the sidebar of page and returns its category tree: one
// node per top-level category with its child categories.
func Fetch(kind, page string) ([]harvestdb.CategoryNode, error) {
	resp, err := crawler.Get(page)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(page)

	var roots []harvestdb.CategoryNode
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "dl" && hasClass(n, "menu-list") {
			root, children := parseCategoryList(n, base)
			if root.URL != "" {
				root.Children = children
				roots = append(roots, root)
			}
			return // children already consumed
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return roots, nil
}

func parseCategoryList(dl *html.Node, base *url.URL) (harvestdb.CategoryNode, []harvestdb.CategoryNode) {
	var root harvestdb.CategoryNode
	seen := map[string]bool{}
	var children []harvestdb.CategoryNode
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case hasClass(n, "menu-reclassify") && root.URL == "":
				root = harvestdb.CategoryNode{Name: titleOrText(n), URL: resolveURL(base, href(n))}
			case hasClass(n, "menu-three-classify"):
				u := resolveURL(base, href(n))
				if u == "" || seen[u] {
					break
				}
				seen[u] = true
				children = append(children, harvestdb.CategoryNode{Name: titleOrText(n), URL: u})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(dl)
	return root, children
}

func hasClass(n *html.Node, cls string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" && strings.Contains(a.Val, cls) {
			return true
		}
	}
	return false
}

func href(n *html.Node) string {
	for _, a := range n.Attr {
		if a.Key == "href" {
			return a.Val
		}
	}
	return ""
}

func titleOrText(n *html.Node) string {
	for _, a := range n.Attr {
		if a.Key == "title" && strings.TrimSpace(a.Val) != "" {
			return strings.TrimSpace(a.Val)
		}
	}
	return strings.TrimSpace(n.FirstChild.Data)
}

func resolveURL(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	abs, err := base.Parse(ref)
	if err != nil {
		return ""
	}
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	abs.Fragment = ""
	return abs.String()
}
