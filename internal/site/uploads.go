package site

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// Uploads are served only if they are known media files (including the
// generated sizes) or referenced by published content. WordPress' uploads
// directory also holds plugin data (caches, logs, form uploads) that
// Apache protected with .htaccess rules; none of that must become public.

// uploadExtensions are the file types served from the uploads directory.
var uploadExtensions = map[string]string{
	".jpg": "image", ".jpeg": "image", ".png": "image", ".gif": "image", ".webp": "image", ".avif": "image",
	".ico": "image", ".bmp": "image", ".svg": "svg",
	".pdf": "download", ".zip": "download", ".gz": "download", ".tgz": "download", ".bz2": "download",
	".xz": "download", ".7z": "download", ".tar": "download", ".deb": "download", ".rpm": "download",
	".mp4": "media", ".webm": "media", ".mp3": "media", ".ogg": "media", ".m4a": "media", ".wav": "media",
}

var reUploadRef = regexp.MustCompile(`/wp-content/uploads/([^"'\s<>()?#\\]+)`)

// uploadAllowlist returns the set of servable upload paths.
func uploadAllowlist(ctx context.Context, q db.Querier, s *Settings) (map[string]bool, error) {
	allow := map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT m.file, m.sizes FROM media m JOIN posts p ON p.id = m.post_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var file, sizes string
		if err := rows.Scan(&file, &sizes); err != nil {
			rows.Close()
			return nil, err
		}
		allow[file] = true
		var sz map[string]db.MediaSize
		if json.Unmarshal([]byte(sizes), &sz) == nil {
			dir := path.Dir(file)
			for _, v := range sz {
				allow[path.Join(dir, v.File)] = true
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	addRefs := func(text string) {
		for _, m := range reUploadRef.FindAllStringSubmatch(text, -1) {
			allow[path.Clean(m[1])] = true
		}
	}
	rows, err = q.QueryContext(ctx, `SELECT content FROM posts WHERE status = 'publish' UNION ALL SELECT settings FROM widgets`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return nil, err
		}
		addRefs(text)
	}
	rows.Close()
	addRefs(s.ThemeMod("background_image"))
	addRefs(s.ThemeMod("header_image"))
	return allow, rows.Err()
}

// serveUpload serves /wp-content/uploads/<file> if it is allowlisted.
func (s *Site) serveUpload(w http.ResponseWriter, r *http.Request, st *state) {
	if s.uploadsRoot == nil {
		http.NotFound(w, r)
		return
	}
	file := strings.TrimPrefix(r.URL.Path, "/wp-content/uploads/")
	clean := path.Clean("/" + file)[1:]
	if clean != file || file == "" {
		http.NotFound(w, r)
		return
	}
	for _, seg := range strings.Split(file, "/") {
		if strings.HasPrefix(seg, ".") {
			http.NotFound(w, r)
			return
		}
	}
	kind, ok := uploadExtensions[strings.ToLower(path.Ext(file))]
	if !ok || !st.uploads[file] {
		http.NotFound(w, r)
		return
	}
	f, err := s.uploadsRoot.Open(file)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("open upload", "file", file, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "public, max-age=2592000")
	switch kind {
	case "svg":
		// SVG can carry scripts; render it as an isolated document.
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	case "download":
		h.Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// openUploadsRoot opens the uploads directory as an os.Root, which keeps
// all access (including symlinks) inside the directory.
func openUploadsRoot(dir string) *os.Root {
	if dir == "" {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	return root
}
