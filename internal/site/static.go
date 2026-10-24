package site

import (
	"io/fs"
	"net/http"
	"os"
	"path"
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

// uploadsHandler serves /wp-content/uploads/ from dir without directory
// listings.
func uploadsHandler(dir string) http.Handler {
	if dir == "" {
		return http.NotFoundHandler()
	}
	fsys := noDirFS{os.DirFS(dir)}
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := path.Clean(strings.TrimPrefix(r.URL.Path, "/wp-content/uploads"))
		if file == "/" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		r2 := r.Clone(r.Context())
		r2.URL.Path = file
		files.ServeHTTP(w, r2)
	})
}

// noDirFS hides directories so http.FileServer cannot list them.
type noDirFS struct{ fs.FS }

func (n noDirFS) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}
