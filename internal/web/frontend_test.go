package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"cineroute/internal/adminui"
)

// Exercise the embedded SPA as served by Go, including assets before login.
func TestAdminFrontendBundle(t *testing.T) {
	if !adminui.Built() {
		t.Skip("run make admin to test the compiled Svelte application")
	}
	cfg := requestTestConfig(t)
	cfg.AuthPassword = "admin-secret"
	handler := New(cfg, nil, nil).Handler()
	response := requestCall(t, handler, http.MethodGet, "/", nil, false)
	page := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(page, `id="app"`) || !strings.Contains(page, `type="module"`) {
		t.Fatalf("admin Svelte entry unavailable: %d %s", response.Code, page)
	}
	if strings.Contains(page, "onclick=") || strings.Contains(page, "/requests.js") {
		t.Fatal("retired template UI is still served")
	}
	assets := regexp.MustCompile(`(?:src|href)="(/admin/assets/[^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(assets) < 2 {
		t.Fatal("compiled script and stylesheet missing")
	}
	for _, match := range assets {
		asset := requestCall(t, handler, http.MethodGet, match[1], nil, false)
		if asset.Code != http.StatusOK || asset.Body.Len() == 0 || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("Svelte asset %s unavailable or uncached: %d", match[1], asset.Code)
		}
	}
	for _, path := range []string{"/requests.js", "/catalog.js", "/app.css", "/admin.css", "/admin/assets/missing.js"} {
		asset := requestCall(t, handler, http.MethodGet, path, nil, true)
		if asset.Code != http.StatusNotFound {
			t.Fatalf("retired or missing asset %s returned %d", path, asset.Code)
		}
	}
}
