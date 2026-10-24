package render

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseShortcodeAttrs(t *testing.T) {
	attrs, pos := ParseShortcodeAttrs(` ids="1,2" Columns='4' size=large flag "quoted" bad="<b"`)
	want := map[string]string{"ids": "1,2", "columns": "4", "size": "large", "bad": ""}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("attrs = %v, want %v", attrs, want)
	}
	if !reflect.DeepEqual(pos, []string{"flag", "quoted"}) {
		t.Errorf("positional = %v", pos)
	}
}

func TestDoShortcodes(t *testing.T) {
	reg := map[string]ShortcodeFunc{
		"x": func(env *Env, sc *Shortcode) string {
			return "<" + sc.Attr("a", "") + "|" + sc.Content + ">"
		},
	}
	tests := []struct{ in, want string }{
		{`a [x a="1"] b`, `a <1|> b`},
		{`[x a=2]inner <b>bold</b>[/x]`, `<2|inner <b>bold</b>>`},
		{`[x/] [[x]] [y]`, `<|> [x] [y]`},
		{`<a title="[x]">t</a>`, `<a title="[x]">t</a>`},
		{`[xy] [x-y]`, `[xy] [x-y]`},
	}
	for _, tt := range tests {
		if got := DoShortcodes(&Env{}, tt.in, reg); got != tt.want {
			t.Errorf("DoShortcodes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := StripShortcodes(`a [x a=1]c[/x] b`, reg); got != "a  b" {
		t.Errorf("StripShortcodes = %q", got)
	}
}

func TestContentMore(t *testing.T) {
	p := &Pipeline{Shortcodes: map[string]ShortcodeFunc{}}
	in := "<p>Teaser</p>\n<!--more-->\n<p>Rest</p>"
	got := p.Content(&Env{}, in, 7, ContentOptions{Teaser: true, MoreText: "More", URL: "/p/"})
	if !strings.Contains(got, `<a href="/p/#more-7" class="more-link">More</a>`) || strings.Contains(got, "Rest") {
		t.Errorf("teaser = %q", got)
	}
	got = p.Content(&Env{}, in, 7, ContentOptions{})
	if !strings.Contains(got, `<span id="more-7"></span>`) || !strings.Contains(got, "Rest") {
		t.Errorf("full = %q", got)
	}
}

func TestSrcsetRatio(t *testing.T) {
	if !imageMatchesRatio(300, 216, 768, 553) || imageMatchesRatio(300, 216, 150, 150) {
		t.Error("imageMatchesRatio")
	}
}
