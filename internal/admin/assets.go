package admin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"

	webadmin "github.com/theinvisible/gopress/web/admin"
)

// assets serves web/admin/static under /admin/static/<version>/. One
// version hash covers all files, so relative url()s between the WordPress
// stylesheets keep working and every release busts all caches at once.
type assets struct {
	version string
	files   map[string]*asset
}

// asset is a static file with precompressed variants for text types.
type asset struct {
	ct          string
	raw, gz, br []byte
}

func loadAssets() (*assets, error) {
	static, err := fs.Sub(webadmin.FS, "static")
	if err != nil {
		return nil, err
	}
	a := &assets{files: map[string]*asset{}}
	h := sha256.New()
	err = fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(data)
		f := &asset{ct: mime.TypeByExtension(path.Ext(p)), raw: data}
		if f.ct == "" {
			f.ct = "application/octet-stream"
		}
		if compressible(f.ct) && len(data) > 512 {
			f.gz, f.br = compress(data)
		}
		a.files[p] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.version = hex.EncodeToString(h.Sum(nil)[:5])
	return a, nil
}

// URL returns the versioned URL of a static file.
func (a *assets) URL(file string) string {
	return "/admin/static/" + a.version + "/" + strings.TrimPrefix(file, "/")
}

func (a *assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/static/")
	version, file, ok := strings.Cut(rest, "/")
	file = strings.TrimPrefix(path.Clean("/"+file), "/")
	f := a.files[file]
	if !ok || f == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ct)
	h.Add("Vary", "Accept-Encoding")
	data := f.raw
	if f.br != nil {
		switch ae := r.Header.Get("Accept-Encoding"); {
		case strings.Contains(ae, "br"):
			data = f.br
			h.Set("Content-Encoding", "br")
		case strings.Contains(ae, "gzip"):
			data = f.gz
			h.Set("Content-Encoding", "gzip")
		}
	}
	h.Set("X-Content-Type-Options", "nosniff")
	if version == a.version {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func compressible(ct string) bool {
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") || strings.Contains(ct, "svg") ||
		strings.Contains(ct, "json")
}

func compress(data []byte) (gz, br []byte) {
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	zw.Write(data)
	zw.Close()
	gz = bytes.Clone(b.Bytes())
	b.Reset()
	bw := brotli.NewWriterLevel(&b, 9)
	bw.Write(data)
	bw.Close()
	return gz, b.Bytes()
}
