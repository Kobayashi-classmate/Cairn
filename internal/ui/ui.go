// SPDX-License-Identifier: AGPL-3.0-only

// Package ui serves the single-page web UI. Builds with the "embedui" tag
// embed internal/ui/dist (copied there from web/dist by `make web`); other
// builds serve a placeholder page.
package ui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const placeholder = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Cairn</title></head>
<body><h1>Cairn is running</h1>
<p>This build does not include the web UI. Build it with <code>make web go</code>.</p></body></html>
`

// Handler serves static files, and index.html for any other non-API path so
// the client-side router can handle it (history mode).
func Handler() http.Handler {
	fsys, ok := distFS()
	if !ok {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholder))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Cleaning a rooted path removes every "..", and fs.ValidPath rejects
		// anything else unusual; the FS is the embedded build output anyway.
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && fs.ValidPath(name) {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts content hashes in asset file names.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				http.ServeFileFS(w, r, fsys, name) //nolint:gosec // G703: name is cleaned and validated above.
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, fsys, "index.html")
	})
}
