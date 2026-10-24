package render

import (
	"strings"
	"testing"
)

func TestSanitizePost(t *testing.T) {
	for in, want := range map[string]string{
		`<p class="x">Hi <!--more--> there</p>`:                    `<p class="x">Hi <!--more--> there</p>`,
		`<script>alert(1)</script><p>ok</p>`:                       `<p>ok</p>`,
		`<a href="javascript:alert(1)">x</a>`:                      `x`,
		`<img src="/a.jpg" onerror="alert(1)" class="wp-image-5">`: `<img src="/a.jpg" class="wp-image-5">`,
		`[gallery ids="1,2"]`:                                      `[gallery ids="1,2"]`,
		`<iframe src="https://evil.example"></iframe>`:             ``,
		`<p style="text-align: center; position: fixed">c</p>`:     `<p style="text-align: center">c</p>`,
		"\x00GPKEEP0\x00":                                          "GPKEEP0", // injected placeholder stays inert text
	} {
		got := SanitizePost(in)
		if strings.TrimSpace(got) != want {
			t.Errorf("SanitizePost(%q) = %q, want %q", in, got, want)
		}
	}
}
