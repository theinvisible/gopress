package render

import (
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	src := "## Release 1.2\n\n- **fast** startup\n- ~~old~~ flag\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfmt.Println(\"<hi>\")\n```\n\n" +
		"Visit https://example.org or [x](javascript:alert(1)).\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n"
	h, err := MarkdownToHTML(src)
	if err != nil {
		t.Fatal(err)
	}
	out := SanitizePost(h)
	for _, want := range []string{"<h2>Release 1.2</h2>", "<strong>fast</strong>", "<del>old</del>", "<table>", `<code class="language-go">`,
		"&lt;hi&gt;", `<a href="https://example.org"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	for _, bad := range []string{"<script", "onerror", `href="javascript:`} {
		if strings.Contains(out, bad) {
			t.Errorf("%q survived: %s", bad, out)
		}
	}
}
