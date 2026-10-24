package wpimport

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestConvertBlocks(t *testing.T) {
	in := "<!-- wp:paragraph -->\n<p>Hi</p>\n<!-- /wp:paragraph -->\n\n" +
		"<!-- wp:foto-slider-lightbox/gallery {\"galleryId\":791} /-->\n\n" +
		"<!-- wp:more -->\n<!--more-->\n<!-- /wp:more -->\n\n" +
		"<!-- wp:image {\"id\":5} -->\n<figure><img class=\"wp-image-5\"/></figure>\n<!-- /wp:image -->\n" +
		"<!-- regular comment -->"
	got, format, warnings := convertContent(in, "https://example.org")
	if format != "html" {
		t.Errorf("format = %q", format)
	}
	want := "<p class=\"wp-block-paragraph\">Hi</p>\n\n\n[fotogalerie id=\"791\"]\n\n\n<!--more-->\n\n\n\n<figure><img class=\"wp-image-5\"/></figure>\n\n<!-- regular comment -->"
	if got != want {
		t.Errorf("convertContent =\n%q\nwant\n%q", got, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestConvertClassicAndURLs(t *testing.T) {
	in := "See <a href=\"https://example.org/2020/01/x/\">https://example.org/2020/01/x/</a>\n\n[gallery ids=\"1,2\"]"
	got, format, _ := convertContent(in, "https://example.org")
	want := "See <a href=\"/2020/01/x/\">https://example.org/2020/01/x/</a>\n\n[gallery ids=\"1,2\"]"
	if format != "wpautop" {
		t.Errorf("format = %q", format)
	}
	if got != want {
		t.Errorf("convertContent =\n%q\nwant\n%q", got, want)
	}
}

// TestBlocksGolden compares the block conversion with do_blocks() output of
// the reference WordPress (internal/render/testdata/golden.json).
func TestBlocksGolden(t *testing.T) {
	var cases []struct{ Name, In, Blocks string }
	for i, f := range []string{"../render/testdata/golden.json", "../../local/golden-site.json"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			if i == 0 {
				t.Fatal(err)
			}
			continue // site golden data is optional (local only)
		}
		var c []struct{ Name, In, Blocks string }
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, c...)
	}
	norm := regexp.MustCompile(` wp-container-[\w-]+-is-layout-[0-9a-f]+| wp-block-gallery-\d+`)
	for _, c := range cases {
		if c.Blocks == "" || strings.Contains(c.In, "foto-slider-lightbox/gallery") {
			continue
		}
		got, _ := convertBlocks(c.In)
		want := strings.TrimSpace(norm.ReplaceAllString(c.Blocks, ""))
		got = norm.ReplaceAllString(got, "")
		if got != want {
			t.Errorf("%s:\n--- got\n%s\n--- want\n%s", c.Name, got, want)
		}
	}
}

func TestRelativeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.org/wp-content/uploads/a.jpg": "/wp-content/uploads/a.jpg",
		"http://example.org/x":                         "/x",
		"https://other.example/a.jpg":                  "https://other.example/a.jpg",
	} {
		if got := relativeURL(in, "https://example.org"); got != want {
			t.Errorf("relativeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
