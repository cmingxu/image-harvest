package nipic

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sidebar = `<html><body>
<div class="menu-box-bd">
  <dl class="relative menu-list">
    <dt class="menu-title">
      <div class="menu-title-inner">
        <a href="/design/acg/index.html" class="fl mr10 relative menu-reclassify" title="动漫动画"><strong>动漫动画</strong></a>
        <a class="fl menu-three-classify" title="动漫人物" href="/design/acg/renwu/index.html">动漫人物</a>
      </div>
    </dt>
    <dd class="menu-item-list clearfix">
      <a class="fl menu-three-classify" title="动漫人物" href="/design/acg/renwu/index.html">动漫人物</a>
      <a class="fl menu-three-classify" title="GIF动画" href="/design/acg/gif/index.html">GIF动画</a>
    </dd>
  </dl>
</div>
</body></html>`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sidebar)
	}))
	defer srv.Close()

	roots, err := Fetch("design", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 {
		t.Fatalf("want 1 root, got %d: %+v", len(roots), roots)
	}
	r := roots[0]
	if r.Name != "动漫动画" || r.URL != srv.URL+"/design/acg/index.html" {
		t.Fatalf("unexpected root: %+v", r)
	}
	if len(r.Children) != 2 { // 动漫人物 appears in dt+dd, must dedupe
		t.Fatalf("want 2 children, got %d: %+v", len(r.Children), r.Children)
	}
	if r.Children[0].Name != "动漫人物" || r.Children[1].Name != "GIF动画" {
		t.Fatalf("unexpected children: %+v", r.Children)
	}
}
