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

func TestSanitizePluginURLs(t *testing.T) {
	in := `<video poster="javascript:alert(1)" src="/v.mp4"></video><img src="/a.png" srcset="javascript:x 1x, /b.png 2x">` +
		`<img src="/c.png" srcset="/c.png 1x, https://cdn.example/c2.png 2x"><input type="image" src="javascript:x">` +
		`<source src="data:text/html,x"><form action="/\evil.example/x"></form><form action="//evil.example"></form><form action="/donate"></form>`
	out := SanitizePluginHTML(in)
	for _, bad := range []string{"javascript:", "data:text/html", `\evil`, "//evil"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q survived: %s", bad, out)
		}
	}
	for _, want := range []string{`src="/v.mp4"`, `srcset="/c.png 1x, https://cdn.example/c2.png 2x"`, `action="/donate"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	admin := SanitizePluginAdminHTML(`<div data-comment-actions data-id="../../posts/5" class="x" id="y"><button data-moderate="delete">Stats</button>` +
		`<form action="/admin/x"><input name="a"></form><p style="color:red">ok <a href="/admin/">link</a></p></div>`)
	for _, bad := range []string{"data-", "class=", "id=", "<button", "<form", "<input", "style="} {
		if strings.Contains(admin, bad) {
			t.Errorf("admin policy kept %q: %s", bad, admin)
		}
	}
	if !strings.Contains(admin, `<a href="/admin/">link</a>`) {
		t.Errorf("admin policy: %s", admin)
	}
}
