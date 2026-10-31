// Package adminui embeds the Svelte admin application. Node is build-only.
package adminui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var assets embed.FS

// Built reports whether the Svelte build is present in the embedded files.
func Built() bool { _, err := assets.ReadFile("dist/index.html"); return err == nil }

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean("/" + r.URL.Path)
		if name == "/" {
			data, err := fs.ReadFile(root, "index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`<!doctype html><html lang="en"><title>CineRoute Administration</title><p>Build the Svelte admin with <code>make admin</code>, then rebuild CineRoute.</p></html>`))
				return
			}
			w.Write(data)
			return
		}
		if name == "/index.html" || name == "/placeholder.txt" {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(root, strings.TrimPrefix(name, "/")); err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
