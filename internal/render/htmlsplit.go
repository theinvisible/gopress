// Package render implements the content filters WordPress applies to post
// content (wpautop, wptexturize, shortcodes, image tags, ...). The functions
// are ports of the PHP originals in wp-includes/formatting.php and friends;
// regexes that RE2 cannot express (lookbehind, backreferences, possessive
// quantifiers) are replaced by hand-written scanners.
package render

import "strings"

// htmlSplit splits s into alternating text and element parts, like
// wp_html_split(): parts[0] is text, parts[1] an element (tag, comment or
// CDATA section), parts[2] text, and so on. The result always has an odd
// length; joining it yields s.
func htmlSplit(s string) []string {
	parts := make([]string, 0, 16)
	textStart := 0
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			i++
			continue
		}
		end := elementEnd(s, i)
		parts = append(parts, s[textStart:i], s[i:end])
		i = end
		textStart = end
	}
	return append(parts, s[textStart:])
}

// elementEnd returns the end offset of the element starting at s[i] == '<'.
// Comments run to "-->" and CDATA to "]]>" (or the end of input); other
// elements run to the next '>' inclusive, or the end of input.
func elementEnd(s string, i int) int {
	rest := s[i+1:]
	switch {
	case strings.HasPrefix(rest, "!--"):
		if j := strings.Index(rest[3:], "-->"); j >= 0 {
			return i + 1 + 3 + j + 3
		}
		return len(s)
	case strings.HasPrefix(rest, "![CDATA["):
		if j := strings.Index(rest[8:], "]]>"); j >= 0 {
			return i + 1 + 8 + j + 3
		}
		return len(s)
	}
	if j := strings.IndexByte(rest, '>'); j >= 0 {
		return i + 1 + j + 1
	}
	return len(s)
}

// replaceInHTMLTags replaces old with new inside elements only, like
// wp_replace_in_html_tags() with a single pair.
func replaceInHTMLTags(s, old, new string) string {
	if !strings.Contains(s, old) {
		return s
	}
	parts := htmlSplit(s)
	changed := false
	for i := 1; i < len(parts); i += 2 {
		if strings.Contains(parts[i], old) {
			parts[i] = strings.ReplaceAll(parts[i], old, new)
			changed = true
		}
	}
	if !changed {
		return s
	}
	return strings.Join(parts, "")
}
