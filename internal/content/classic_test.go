package content

import (
	"context"
	"encoding/json"
	"html"
	"sort"
	"unicode"

	xhtml "golang.org/x/net/html"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

type stubLinks struct{}

func (stubLinks) PostURL(p *db.Post) string    { return "/?p=" + itoa(p.ID) }
func (stubLinks) UploadURL(file string) string { return "/wp-content/uploads/" + file }

func itoa(n int64) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n)) }

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// inlineMarks are the formatting elements compared as text runs.
var inlineMarks = map[string]bool{"strong": true, "b": true, "em": true, "i": true, "s": true, "del": true,
	"strike": true, "u": true, "code": true, "a": true, "span": true, "mark": true, "sup": true, "sub": true,
	"small": true, "abbr": true, "kbd": true, "cite": true, "q": true, "ins": true}

var markAlias = map[string]string{"b": "strong", "i": "em", "del": "s", "strike": "s"}

// normalizeRendered makes equivalent HTML equal (like canonical() in
// web/editor/src/content.js): text is compared as runs with their set of
// inline formats, so nesting order does not matter; attributes are sorted;
// character references are decoded; whitespace is collapsed; empty
// formatting elements (like an empty link) are invisible and dropped.
func normalizeRendered(s string) string {
	z := xhtml.NewTokenizer(strings.NewReader(s))
	var out []string
	var marks []string
	lastMarks, pending := "", ""
	flush := func() {
		if pending != "" {
			out = append(out, "{"+lastMarks+"}"+pending)
			pending = ""
		}
	}
	inPre := 0
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		tok := z.Token()
		name := tok.Data
		if a, ok := markAlias[name]; ok {
			name = a
		}
		switch tt {
		case xhtml.TextToken:
			t := strings.ReplaceAll(tok.Data, "\u00a0", " ")
			if inPre == 0 {
				t = strings.Join(strings.Fields(" "+t+" "), " ")
				if strings.TrimSpace(tok.Data) != "" {
					if unicode.IsSpace(rune(tok.Data[0])) {
						t = " " + t
					}
					if unicode.IsSpace(rune(tok.Data[len(tok.Data)-1])) {
						t += " "
					}
				} else {
					t = " "
				}
			}
			key := strings.Join(sortedCopy(marks), "|")
			if inPre == 0 && strings.TrimSpace(t) == "" {
				// Formatting on whitespace is invisible; runs of spaces collapse.
				if strings.HasSuffix(pending, " ") {
					continue
				}
				key = lastMarks
			}
			if key != lastMarks {
				flush()
				lastMarks = key
			}
			pending += html.EscapeString(t)
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			attrs := make([]string, 0, len(tok.Attr))
			for _, a := range tok.Attr {
				if a.Key == "fetchpriority" || a.Key == "loading" || a.Key == "decoding" {
					continue // depend on the position of the image on the page
				}
				attrs = append(attrs, a.Key+"="+strconvQuote(a.Val))
			}
			sort.Strings(attrs)
			desc := strings.TrimSpace(name + " " + strings.Join(attrs, " "))
			if inlineMarks[name] && tt == xhtml.StartTagToken {
				marks = append(marks, desc)
				continue
			}
			flush()
			out = append(out, "{"+strings.Join(sortedCopy(marks), "|")+"}<"+desc+">")
			if name == "pre" && tt == xhtml.StartTagToken {
				inPre++
			}
		case xhtml.EndTagToken:
			if inlineMarks[name] {
				for i := len(marks) - 1; i >= 0; i-- {
					if strings.HasPrefix(marks[i], name+" ") || marks[i] == name {
						marks = append(marks[:i], marks[i+1:]...)
						break
					}
				}
				continue
			}
			flush()
			out = append(out, "</"+name+">")
			if name == "pre" && inPre > 0 {
				inPre--
			}
		case xhtml.CommentToken:
			flush()
			out = append(out, "<!--"+tok.Data+"-->")
		}
	}
	flush()
	j := strings.Join(out, "")
	// Whitespace next to tags (block boundaries, line breaks) is insignificant here.
	j = regexp.MustCompile(`\{[^}]*\} (\{[^}]*\}<)`).ReplaceAllString(j, "$1")
	j = regexp.MustCompile(`(>)(\{[^}]*\}) `).ReplaceAllString(j, "$1$2")
	j = regexp.MustCompile(`\{[^}]*\} ?(</|<!--|$)`).ReplaceAllString(j, "$1")
	j = regexp.MustCompile(` (</)`).ReplaceAllString(j, "$1")
	j = regexp.MustCompile(`\{[^{}]*\}(\{)`).ReplaceAllString(j, "$1")
	j = regexp.MustCompile(`\{[^{}]*\}(\{)`).ReplaceAllString(j, "$1")
	return j
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

func strconvQuote(s string) string { return `"` + html.EscapeString(s) + `"` }

// TestClassicToHTMLRendersTheSame converts every classic post of the local
// reference data (local/data, not committed) to the HTML the editor stores
// and checks that the rendered content does not change. Differences can only
// come from wptexturize running before wpautop for classic content; they
// are listed.
func TestClassicToHTMLRendersTheSame(t *testing.T) {
	const path = "../../local/data/gopress.db"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no local reference data")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pipe := render.NewPipeline()
	pipe.UseSmilies = true
	checked, differ := 0, 0
	for _, typ := range []string{db.TypePost, db.TypePage} {
		posts, _, err := db.AdminPosts(ctx, d.R, db.AdminPostQuery{Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		for i := range posts {
			p := &posts[i]
			if p.ContentFormat != db.FormatWPAutop {
				continue
			}
			checked++
			env := func() *render.Env { return &render.Env{Ctx: ctx, Q: d.R, Links: stubLinks{}, Post: p} }
			before := pipe.Content(env(), p.Content, db.FormatWPAutop, p.ID, render.ContentOptions{})
			after := pipe.Content(env(), ClassicToHTML(p.Content), db.FormatHTML, p.ID, render.ContentOptions{})
			a, b := normalizeRendered(before), normalizeRendered(after)
			if a != b {
				differ++
				i := 0
				for i < len(a) && i < len(b) && a[i] == b[i] {
					i++
				}
				t.Logf("post %d %q differs at %d:\n  classic: …%s\n  html:    …%s", p.ID, p.Title, i,
					a[max(0, i-60):min(len(a), i+80)], b[max(0, i-60):min(len(b), i+80)])
			}
		}
	}
	t.Logf("%d classic documents checked, %d render differently", checked, differ)
	if differ > 0 {
		t.Fail()
	}
}

// TestEditorRoundTripRendersTheSame compares the rendered content of every
// local post before and after a round trip through the visual editor
// (local/editor-roundtrip.json, written by web/editor/test/roundtrip.mjs).
func TestEditorRoundTripRendersTheSame(t *testing.T) {
	const path = "../../local/data/gopress.db"
	data, err := os.ReadFile("../../local/editor-roundtrip.json")
	if err != nil {
		t.Skip("no editor round-trip output (run web/editor/test/roundtrip.mjs)")
	}
	var outs []struct {
		ID   int64
		HTML string
	}
	if err := json.Unmarshal(data, &outs); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pipe := render.NewPipeline()
	pipe.UseSmilies = true
	differ := 0
	for _, o := range outs {
		p, err := db.GetPost(ctx, d.R, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		env := func() *render.Env { return &render.Env{Ctx: ctx, Q: d.R, Links: stubLinks{}, Post: p} }
		before := pipe.Content(env(), p.Content, p.ContentFormat, p.ID, render.ContentOptions{})
		after := pipe.Content(env(), o.HTML, db.FormatHTML, p.ID, render.ContentOptions{})
		a, b := normalizeRendered(before), normalizeRendered(after)
		if a != b {
			differ++
			i := 0
			for i < len(a) && i < len(b) && a[i] == b[i] {
				i++
			}
			t.Logf("post %d %q differs at %d:\n  stored: …%s\n  editor: …%s", p.ID, p.Title, i,
				a[max(0, i-80):min(len(a), i+80)], b[max(0, i-80):min(len(b), i+80)])
		}
	}
	t.Logf("%d documents checked, %d render differently", len(outs), differ)
	if differ > 0 {
		t.Fail()
	}
}
