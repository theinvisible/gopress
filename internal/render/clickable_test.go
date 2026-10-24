package render

import (
	"strings"
	"testing"
)

func TestMakeClickable(t *testing.T) {
	tests := []struct{ in, want string }{
		{"see https://example.org/a.", `see <a href="https://example.org/a" rel="nofollow">https://example.org/a</a>.`},
		{"(http://x.org/y_(z))", `(<a href="http://x.org/y_(z)" rel="nofollow">http://x.org/y_(z)</a>)`},
		{"www.example.org, mail me@example.org", `<a href="http://www.example.org" rel="nofollow">http://www.example.org</a>, mail <a href="mailto:me@example.org">me@example.org</a>`},
		{`<a href="https://a.b">https://a.b</a>`, `<a href="https://a.b">https://a.b</a>`},
		{"<code>http://x.y</code>", "<code>http://x.y</code>"},
		{"https://hadler.me/x", `<a href="https://hadler.me/x">https://hadler.me/x</a>`},
		{`javascript://%0aalert(1)`, `javascript://%0aalert(1)`},
	}
	for _, tt := range tests {
		if got := MakeClickable(tt.in, "hadler.me"); got != tt.want {
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
