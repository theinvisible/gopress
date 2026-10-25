package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Plugin templates: a package may hold templates/*.html (Go html/template
// syntax). The host hands them to the plugin (host function "templates"),
// with the active theme's override of a file (plugins/<plugin>/<file> in
// the theme) in its place; the plugin renders them inside its sandbox, so
// a template cannot run longer or use more memory than the plugin may.
// The result is filtered like any plugin HTML.

const maxTemplateBytes = 64 << 10

var reTemplate = regexp.MustCompile(`^templates/[a-z0-9][a-z0-9_-]{0,63}\.html$`)

// checkTemplates parses the package's templates as one set (they can
// call each other by file name), so a broken one is refused on upload.
// Parsing runs no template code.
func checkTemplates(files map[string][]byte) error {
	set := template.New("")
	for _, name := range templateNames(files) {
		if _, err := set.New(path.Base(name)).Parse(string(files[name])); err != nil {
			return fmt.Errorf("template %s: %w", name, err)
		}
	}
	return nil
}

func templateNames(files map[string][]byte) []string {
	var names []string
	for name := range files {
		if strings.HasPrefix(name, "templates/") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// templateFiles are a plugin's templates for the active theme and their
// version (a hash: the plugin keeps its parsed set while it matches).
type templateFiles struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files,omitempty"` // file name → source
}

func (p *plugin) templates() (*templateFiles, error) {
	themeName := p.h.opts.Site.ThemeName()
	p.tplMu.Lock()
	defer p.tplMu.Unlock()
	if t, ok := p.tplCache[themeName]; ok {
		return t, nil
	}
	t := &templateFiles{Files: map[string]string{}}
	h := sha256.New()
	for _, name := range templateNames(p.pkg.files) {
		file := path.Base(name)
		src := p.pkg.files[name]
		if o, ok := p.h.opts.Site.PluginTemplate(p.pkg.Manifest.Name, file); ok {
			if len(o) > maxTemplateBytes {
				return nil, userErr("the theme's override of " + file + " is larger than 64 KB")
			}
			src = o
		}
		t.Files[file] = string(src)
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", file, len(src), src)
	}
	t.Version = hex.EncodeToString(h.Sum(nil))[:16]
	if p.tplCache == nil {
		p.tplCache = map[string]*templateFiles{}
	}
	p.tplCache[themeName] = t
	return t, nil
}
