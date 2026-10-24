package site

import (
	"net/http"
	"strings"

	"github.com/theinvisible/gopress/internal/theme"
)

// staticHandler serves /static/<theme>/<hash>/<file>. Any hash is
// accepted (relative URLs inside CSS keep working); the current hash gets
// an immutable cache header.
func staticHandler(th *theme.Theme) http.Handler {
	files := http.FileServerFS(th.Static())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/static/"+th.Name+"/")
		hash, file, ok := strings.Cut(rest, "/")
		if !ok || rest == r.URL.Path || file == "" || strings.HasSuffix(file, "/") {
			http.NotFound(w, r)
			return
		}
		if th.AssetPath(file) == "/static/"+th.Name+"/"+hash+"/"+file {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + file
		files.ServeHTTP(w, r2)
	})
}
