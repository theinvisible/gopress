package wpimport

import "testing"

func TestConvertBlocks(t *testing.T) {
	in := "<!-- wp:paragraph -->\n<p>Hi</p>\n<!-- /wp:paragraph -->\n\n" +
		"<!-- wp:foto-slider-lightbox/gallery {\"galleryId\":791} /-->\n\n" +
		"<!-- wp:more -->\n<!--more-->\n<!-- /wp:more -->\n\n" +
		"<!-- wp:image {\"id\":5} -->\n<figure><img class=\"wp-image-5\"/></figure>\n<!-- /wp:image -->\n" +
		"<!-- regular comment -->"
	got, warnings := convertContent(in, "https://example.org")
	want := "<p>Hi</p>\n\n\n[fotogalerie id=\"791\"]\n\n<!--more-->\n\n\n<figure><img class=\"wp-image-5\"/></figure>\n\n<!-- regular comment -->"
	if got != want {
		t.Errorf("convertContent =\n%q\nwant\n%q", got, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestConvertClassicAndURLs(t *testing.T) {
	in := "See <a href=\"https://example.org/2020/01/x/\">https://example.org/2020/01/x/</a>\n\n[gallery ids=\"1,2\"]"
	got, _ := convertContent(in, "https://example.org")
	want := "<p>See <a href=\"/2020/01/x/\">https://example.org/2020/01/x/</a></p>\n[gallery ids=\"1,2\"]\n"
	if got != want {
		t.Errorf("convertContent =\n%q\nwant\n%q", got, want)
	}
}
