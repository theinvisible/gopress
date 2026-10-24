package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	webadmin "github.com/theinvisible/gopress/web/admin"
)

// templates holds one parsed template set per page: the shared partials
// (templates/_*.html) plus the page's own file.
type templates struct {
	pages map[string]*template.Template
}

func loadTemplates(as *assets) (*templates, error) {
	root, err := fs.Sub(webadmin.FS, "templates")
	if err != nil {
		return nil, err
	}
	base := template.New("admin").Funcs(template.FuncMap{
		"asset":    as.URL,
		"avatar":   avatarURL,
		"json":     toJSON,
		"plural":   plural,
		"title":    escTitle,
		"lower":    strings.ToLower,
		"urlLabel": urlLabel,
		"iterate":  func(n int) []int { return make([]int, max(n, 0)) },
		"add":      func(a, b int) int { return a + b },
		"query":    withQuery,
		"unixTime": func(t int64) string { return time.Unix(t, 0).UTC().Format("2006/01/02 15:04 UTC") },
		"contains": func(list []string, s string) bool {
			for _, x := range list {
				if x == s {
					return true
				}
			}
			return false
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
	})
	shared, err := fs.Glob(root, "_*.html")
	if err != nil {
		return nil, err
	}
	if base, err = base.ParseFS(root, shared...); err != nil {
		return nil, err
	}
	all, err := fs.Glob(root, "*.html")
	if err != nil {
		return nil, err
	}
	t := &templates{pages: map[string]*template.Template{}}
	for _, f := range all {
		if strings.HasPrefix(f, "_") {
			continue
		}
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := clone.ParseFS(root, f); err != nil {
			return nil, fmt.Errorf("admin template %s: %w", f, err)
		}
		t.pages[strings.TrimSuffix(path.Base(f), ".html")] = clone
	}
	return t, nil
}

// render executes the page template name (its "page" definition) into w.
func (t *templates) render(w io.Writer, name string, data any) error {
	tpl := t.pages[name]
	if tpl == nil {
		return fmt.Errorf("admin: unknown template %q", name)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "page", data); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// render writes a full admin page.
func (a *Admin) render(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return a.tpl.render(w, name, data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func toJSON(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	return template.JS(b), err
}

// avatarURL returns the Gravatar URL of an email (SHA-256 hash, "mystery"
// fallback), like get_avatar_url().
func avatarURL(email string, size int) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "https://secure.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?s=" + strconv.Itoa(size) + "&d=mm&r=g"
}

var reEscapedEntity = regexp.MustCompile(`&amp;(#[0-9]+|#[xX][0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);`)

// escTitle escapes a title like esc_html(): HTML is shown as text, but
// existing entities (titles are stored as HTML) are not encoded twice.
func escTitle(s string) template.HTML {
	return template.HTML(reEscapedEntity.ReplaceAllString(html.EscapeString(s), "&$1;"))
}

// urlLabel shortens a URL for display (host and path, without scheme).
func urlLabel(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	s = strings.TrimPrefix(strings.TrimSuffix(s, "/"), "www.")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:39]) + "…"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// withQuery returns base with the query parameters kv (key, value, ...)
// set; empty values remove the parameter.
func withQuery(base string, kv ...any) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for i := 0; i+1 < len(kv); i += 2 {
		k, v := fmt.Sprint(kv[i]), fmt.Sprint(kv[i+1])
		if v == "" || v == "0" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
