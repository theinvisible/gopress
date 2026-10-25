package gopress

import (
	"bytes"
	"fmt"
	"html/template"
	"sync"
)

// Templates: a package may hold templates/*.html in Go's html/template
// syntax (values are escaped by context). Template renders one of them;
// the active theme may replace a file (plugins/<plugin>/<file> in the
// theme), and GoPress hands the plugin the theme's version. Templates of
// the package can call each other by file name ({{template "item.html" .}}).

var tpl struct {
	sync.Mutex
	version string
	set     *template.Template
}

// Template renders templates/<name> (e.g. "gallery.html") with data.
func Template(name string, data any) (string, error) {
	set, err := templateSet()
	if err != nil {
		return "", err
	}
	if set.Lookup(name) == nil {
		return "", fmt.Errorf("no template %q", name)
	}
	var b bytes.Buffer
	if err := set.ExecuteTemplate(&b, name, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// templateSet returns the parsed templates, fetched again when the host's
// version changed (another theme).
func templateSet() (*template.Template, error) {
	tpl.Lock()
	defer tpl.Unlock()
	var r struct {
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if err := call("templates", map[string]string{"version": tpl.version}, &r); err != nil {
		return nil, err
	}
	if r.Version == tpl.version && tpl.set != nil {
		return tpl.set, nil
	}
	set := template.New("")
	for name, src := range r.Files {
		if _, err := set.New(name).Parse(src); err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
	}
	tpl.version, tpl.set = r.Version, set
	return set, nil
}
