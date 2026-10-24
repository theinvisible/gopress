// Package theme loads a theme (Go templates + static assets) and renders
// pages from view models prepared by the site package.
package theme

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
)

// Page kinds; each has a templates/<kind>.html defining "main".
var pageKinds = []string{"index", "single", "page", "archive", "search", "404", "attachment"}

// Theme is a loaded theme.
type Theme struct {
	Name   string
	static fs.FS
	pages  map[string]*template.Template

	hashes map[string]string // static path -> short content hash, computed at load
}

// Load loads theme name from fsys (a directory containing <name>/templates
// and <name>/static).
func Load(fsys fs.FS, name string) (*Theme, error) {
	root, err := fs.Sub(fsys, name)
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(root, "static")
	if err != nil {
		return nil, err
	}
	t := &Theme{Name: name, static: static, pages: map[string]*template.Template{}, hashes: map[string]string{}}

	base := template.New("layout").Funcs(template.FuncMap{
		"asset": t.AssetPath,
	})
	shared, err := fs.Glob(root, "templates/_*.html")
	if err != nil {
		return nil, err
	}
	if len(shared) == 0 {
		return nil, fmt.Errorf("theme %s: no templates/_*.html", name)
	}
	if base, err = base.ParseFS(root, shared...); err != nil {
		return nil, fmt.Errorf("theme %s: %w", name, err)
	}
	// Hash every static file once; lookups never add entries, so requests
	// for unknown files cannot grow memory.
	err = fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		t.hashes[p] = hex.EncodeToString(sum[:4])
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, kind := range pageKinds {
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := clone.ParseFS(root, "templates/"+kind+".html"); err != nil {
			return nil, fmt.Errorf("theme %s: %s: %w", name, kind, err)
		}
		t.pages[kind] = clone
	}
	return t, nil
}

// Render renders page kind with data.
func (t *Theme) Render(w io.Writer, kind string, data any) error {
	tpl := t.pages[kind]
	if tpl == nil {
		return fmt.Errorf("theme %s: unknown page kind %q", t.Name, kind)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// Static returns the theme's static files.
func (t *Theme) Static() fs.FS { return t.static }

// AssetPath returns the URL path of a static file, versioned by content
// hash so it can be cached forever: /static/<theme>/<hash>/<file>.
func (t *Theme) AssetPath(file string) string {
	file = strings.TrimPrefix(path.Clean("/"+file), "/")
	h, ok := t.hashes[file]
	if !ok {
		h = "missing"
	}
	return "/static/" + t.Name + "/" + h + "/" + file
}
