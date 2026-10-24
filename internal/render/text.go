package render

import (
	"html"
	"strings"
)

// PlainText strips tags, comments and shortcode brackets from content and
// collapses whitespace, for search indexing and excerpts.
func PlainText(s string) string {
	parts := htmlSplit(s)
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(p)
	}
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}
