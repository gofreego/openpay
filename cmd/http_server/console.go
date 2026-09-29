package http_server

import (
	"io/fs"
	"net/http"
	"strings"
)

// ConsolePrefix is where the admin console is served. Distinct from the API
// under /openpay/v1/, so no console route can shadow an API path.
const ConsolePrefix = "/payments/"

// ConsoleHandler serves the built admin console (ui/dist, embedded in the
// binary by main.go) with an SPA fallback: a path that is not a file gets
// index.html, and the console's router takes it from there.
//
// When the binary was built without the console (ui/dist holds only its
// placeholder), it says so instead of serving an empty page.
func ConsoleHandler(dist fs.FS) http.Handler {
	index, err := fs.ReadFile(dist, "index.html")
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(w, "the admin console is not built into this binary: run `make ui` and rebuild", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path+"/" == ConsolePrefix {
			http.Redirect(w, r, ConsolePrefix, http.StatusMovedPermanently)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, ConsolePrefix)
		if name != "" && !strings.HasSuffix(name, "/") {
			if f, openErr := dist.Open(name); openErr == nil {
				_ = f.Close()
				// Hashed asset names change with their content, so they can be
				// cached forever; index.html is never cached, so a deploy is
				// picked up on the next load.
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.StripPrefix(ConsolePrefix, files).ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
