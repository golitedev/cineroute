// Package publicui embeds CineRoute's statically exported public interface.
package publicui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed all:dist
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean("/" + r.URL.Path)
		switch name {
		case "/":
			name = "/index.html"
		case "/login", "/login/":
			name = "/login.html"
		}
		if _, err := fs.Stat(root, name[1:]); err != nil {
			if name == "/index.html" || name == "/login.html" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><title>CineRoute</title><body><h1>CineRoute</h1><p>Build the public interface with <code>make web</code>, then rebuild CineRoute.</p></body></html>`))
				return
			}
			http.NotFound(w, r)
			return
		}
		// Do not let the file server redirect /index.html back to /.
		if name == "/index.html" || name == "/login.html" {
			data, _ := fs.ReadFile(root, name[1:])
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
			return
		}
		if len(name) > 14 && name[:14] == "/_next/static/" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
