package wpimport

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// TestExcerptSourceGolden compares the excerpt of block content with
// wp_trim_words(excerpt_remove_blocks(strip_shortcodes(…))) of the
// reference WordPress.
func TestExcerptSourceGolden(t *testing.T) {
	var cases []struct{ Name, In, Excerpt string }
	for i, f := range []string{"../render/testdata/golden.json", "../../local/golden-site.json"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			if i == 0 {
				t.Fatal(err)
			}
			continue
		}
		var c []struct{ Name, In, Excerpt string }
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, c...)
	}
	reg := map[string]render.ShortcodeFunc{}
	for k := range shortcodeTags {
		reg[k] = func(*render.Env, *render.Shortcode) string { return "" }
	}
	n := 0
	for _, c := range cases {
		src, ok := excerptSource(c.In, "")
		if !ok {
			continue
		}
		n++
		got := render.TrimWords(render.StripShortcodes(src, reg), 55, render.ExcerptMore)
		if got != c.Excerpt {
			t.Errorf("%s:\n--- got\n%s\n--- want\n%s", c.Name, got, c.Excerpt)
		}
	}
	if n == 0 {
		t.Error("no block cases in the golden data")
	}
}

func TestExcerptSource(t *testing.T) {
	in := "<!-- wp:paragraph -->\n<p>Intro</p>\n<!-- /wp:paragraph -->\n\n" +
		"<!-- wp:list -->\n<ul class=\"wp-block-list\"><!-- wp:list-item -->\n<li>Item</li>\n<!-- /wp:list-item --></ul>\n<!-- /wp:list -->\n\n" +
		"<!-- wp:code -->\n<pre class=\"wp-block-code\"><code>code</code></pre>\n<!-- /wp:code -->\n\n" +
		"<!-- wp:group -->\n<div class=\"wp-block-group\">text<!-- wp:paragraph -->\n<p>Grouped</p>\n<!-- /wp:paragraph --></div>\n<!-- /wp:group -->\n\n" +
		"<!-- wp:paragraph {\"metadata\":{\"blockVisibility\":false}} -->\n<p>Hidden</p>\n<!-- /wp:paragraph -->\n\n" +
		"<!-- wp:more -->\n<!--more-->\n<!-- /wp:more -->\n\n" +
		"<!-- wp:paragraph -->\n<p>After</p>\n<!-- /wp:paragraph -->"
	src, ok := excerptSource(in, "")
	if !ok {
		t.Fatal("block content not recognized")
	}
	for _, want := range []string{"Intro", "Grouped", "<!--more-->", "After"} {
		if !strings.Contains(src, want) {
			t.Errorf("%q missing: %s", want, src)
		}
	}
	for _, gone := range []string{"Item", "code", "Hidden", ">text<"} {
		if strings.Contains(src, gone) {
			t.Errorf("%q kept: %s", gone, src)
		}
	}
	if _, ok := excerptSource("<p>classic</p>", ""); ok {
		t.Error("classic content got an excerpt source")
	}

	// The stored source only applies to the content it was made for.
	meta := db.ExcerptMeta("<p>content</p>", src)
	if got, ok := db.ExcerptFromMeta(meta, "<p>content</p>"); !ok || got != src {
		t.Error("source not found for its content")
	}
	if _, ok := db.ExcerptFromMeta(meta, "<p>edited</p>"); ok {
		t.Error("source used after the content changed")
	}
}
