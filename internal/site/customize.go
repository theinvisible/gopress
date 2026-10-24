package site

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// Theme customizations (Customizer): header text, header image, site
// icon and additional CSS. Without customizations the output stays as
// it was (the imported site renders identically).

// DefaultHeaderTextColor is Twenty Twelve's default-text-color.
const DefaultHeaderTextColor = "515151"

var reHexColor = regexp.MustCompile(`^[0-9a-fA-F]{3}([0-9a-fA-F]{3})?$`)

// headerTextCSS is twentytwelve_header_style().
func (rq *request) headerTextCSS() string {
	color := rq.st.s.ThemeMod("header_textcolor")
	if color == "" || color == DefaultHeaderTextColor {
		return ""
	}
	var b strings.Builder
	b.WriteString("\t<style type=\"text/css\" id=\"twentytwelve-header-css\">\n")
	if color == "blank" {
		b.WriteString("\t\t\t.site-title,\n\t\t.site-description {\n\t\t\tposition: absolute;\n\t\t\tclip: rect(1px 1px 1px 1px); /* IE7 */\n\t\t\tclip: rect(1px, 1px, 1px, 1px);\n\t\t}\n")
	} else if reHexColor.MatchString(color) {
		fmt.Fprintf(&b, "\t\t\t.site-header h1 a,\n\t\t.site-header h2 {\n\t\t\tcolor: #%s;\n\t\t}\n", color)
	} else {
		return ""
	}
	b.WriteString("\t\t</style>\n\t")
	return b.String()
}

// headerImage is the header image link of Twenty Twelve's header.php.
func (rq *request) headerImage() string {
	s := rq.st.s
	img := s.ThemeMod("header_image")
	if img == "" || img == "remove-header" || img == "random-uploaded-image" || img == "random-default-image" {
		return ""
	}
	if strings.HasPrefix(img, "/") && !strings.HasPrefix(img, "//") {
		img = s.Home + img
	}
	if !strings.HasPrefix(img, "http://") && !strings.HasPrefix(img, "https://") {
		return ""
	}
	w, h := 960, 250 // the theme's custom-header size
	if data, ok := s.ThemeMods["header_image_data"].(map[string]any); ok {
		if v, ok := data["width"].(float64); ok && v > 0 {
			w = int(v)
		}
		if v, ok := data["height"].(float64); ok && v > 0 {
			h = int(v)
		}
	}
	return fmt.Sprintf(`<a href="%s"><img src="%s" class="header-image" width="%d" height="%d" alt="%s" /></a>`,
		esc(rq.st.links.Home()), esc(img), w, h, s.Name)
}

// siteIconLinks is wp_site_icon(): the icon in the sizes WordPress
// outputs (generated when the icon is chosen).
func (rq *request) siteIconLinks() string {
	id := rq.st.s.SiteIcon
	if id == 0 {
		return ""
	}
	m, err := db.GetMedia(rq.ctx, rq.site.db.R, id)
	if err != nil || m == nil {
		return ""
	}
	url := func(px int) string {
		if sz, ok := m.Sizes["site_icon-"+strconv.Itoa(px)]; ok {
			dir := ""
			if i := strings.LastIndexByte(m.File, '/'); i >= 0 {
				dir = m.File[:i+1]
			}
			return rq.st.links.UploadURL(dir + sz.File)
		}
		return rq.st.links.UploadURL(m.File)
	}
	return fmt.Sprintf("<link rel=\"icon\" href=\"%s\" sizes=\"32x32\" />\n<link rel=\"icon\" href=\"%s\" sizes=\"192x192\" />\n"+
		"<link rel=\"apple-touch-icon\" href=\"%s\" />\n<meta name=\"msapplication-TileImage\" content=\"%s\" />\n",
		esc(url(32)), esc(url(192)), esc(url(180)), esc(url(270)))
}

// SiteIconSizes are the sizes generated for a site icon.
var SiteIconSizes = []int{32, 180, 192, 270}

var reStyleTag = regexp.MustCompile(`(?i)<\s*/?\s*style`)

// customCSS is wp_custom_css_cb(): Additional CSS without tags.
func (rq *request) customCSS() string {
	css := strings.TrimSpace(rq.st.s.CustomCSS)
	if css == "" {
		return ""
	}
	css = SanitizeCSS(css)
	return "<style id=\"wp-custom-css\">\n\t\t\t" + css + "\t\t</style>\n"
}

var reTags = regexp.MustCompile(`<[^>]*>`)

// SanitizeCSS removes markup from CSS (strip_tags) so it cannot close
// the style element.
func SanitizeCSS(css string) string {
	for {
		c := reTags.ReplaceAllString(css, "")
		c = reStyleTag.ReplaceAllString(c, "")
		if c == css {
			return strings.ReplaceAll(c, "<", "\\3c ")
		}
		css = c
	}
}
