package render

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestMakeClickable(t *testing.T) {
	tests := []struct{ in, want string }{
		{"see https://example.org/a.", `see <a href="https://example.org/a" rel="nofollow">https://example.org/a</a>.`},
		{"(http://x.org/y_(z))", `(<a href="http://x.org/y_(z)" rel="nofollow">http://x.org/y_(z)</a>)`},
		{"www.example.org, mail me@example.org", `<a href="http://www.example.org" rel="nofollow">http://www.example.org</a>, mail <a href="mailto:me@example.org">me@example.org</a>`},
		{`<a href="https://a.b">https://a.b</a>`, `<a href="https://a.b">https://a.b</a>`},
		{"<code>http://x.y</code>", "<code>http://x.y</code>"},
		{"https://my.example/x", `<a href="https://my.example/x">https://my.example/x</a>`},
		{`javascript://%0aalert(1)`, `javascript://%0aalert(1)`},
	}
	for _, tt := range tests {
		if got := MakeClickable(tt.in, "my.example"); got != tt.want {
			t.Errorf("MakeClickable(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeComment(t *testing.T) {
	in := `<p onclick="x()">Hi <a href="javascript:alert(1)">bad</a> <a href="https://ok.example" class="c">ok</a><script>alert(1)</script><img src=x onerror=y></p>`
	got := SanitizeComment(in)
	for _, bad := range []string{"onclick", "javascript:", "<script", "<img", "class="} {
		if strings.Contains(got, bad) {
			t.Errorf("SanitizeComment kept %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, `href="https://ok.example"`) || !strings.Contains(got, `rel="nofollow ugc"`) {
		t.Errorf("SanitizeComment = %s", got)
	}
}

func TestSanitizeCommentRelInjection(t *testing.T) {
	// A " rel=" inside another attribute value must not be treated as the
	// rel attribute (found by the M1 security review).
	in := `<a title="z rel=" href="https://e.com/a/onmouseover=alert(1)//">x</a> <a href="https://f.example" rel="external">y</a>`
	for range 2 { // sanitized on save and again on output
		in = SanitizeComment(in)
	}
	z := html.NewTokenizer(strings.NewReader(in))
	var links [][]html.Attribute
	for tt := z.Next(); tt != html.ErrorToken; tt = z.Next() {
		if tok := z.Token(); tt == html.StartTagToken && tok.Data == "a" {
			links = append(links, tok.Attr)
		}
	}
	if len(links) != 2 {
		t.Fatalf("links: %s", in)
	}
	for _, a := range links[0] {
		switch a.Key {
		case "title", "href":
		case "rel":
			if a.Val != "nofollow ugc" {
				t.Errorf("rel = %q", a.Val)
			}
		default:
			t.Errorf("injected attribute %s=%q in %s", a.Key, a.Val, in)
		}
	}
	if !strings.Contains(in, `rel="external nofollow ugc"`) {
		t.Errorf("rel attributes: %s", in)
	}
}
