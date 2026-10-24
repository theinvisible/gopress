package render

import (
	"regexp"
	"strings"
)

// ExcerptMore is the default excerpt_more string.
const ExcerptMore = " [&hellip;]"

var reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*?>.*?</(script|style)>`)

// StripAllTags is wp_strip_all_tags() without break removal.
func StripAllTags(s string) string {
	s = reScriptStyle.ReplaceAllStringFunc(s, func(m string) string {
		// Only remove when open and close names match, like '\1'.
		sub := reScriptStyle.FindStringSubmatch(m)
		if strings.EqualFold(sub[1], sub[2]) {
			return ""
		}
		return m
	})
	return strings.TrimSpace(StripTags(s))
}

// StripTags approximates PHP's strip_tags(): removes tags and comments; a
// '<' not followed by a tag-like character is kept.
func StripTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '<' || i+1 >= len(s) || !(isWordChar(s[i+1]) || s[i+1] == '/' || s[i+1] == '!' || s[i+1] == '?') {
			b.WriteByte(c)
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				return b.String()
			}
			i += 4 + end + 2
			continue
		}
		// Skip to the closing '>' outside of quotes.
		var quote byte
		j := i + 1
		for ; j < len(s); j++ {
			switch {
			case quote != 0:
				if s[j] == quote {
					quote = 0
				}
			case s[j] == '"' || s[j] == '\'':
				quote = s[j]
			case s[j] == '>':
				goto done
			}
		}
	done:
		i = j
	}
	return b.String()
}

// TrimWords is wp_trim_words() for space-separated languages.
func TrimWords(text string, n int, more string) string {
	text = StripAllTags(text)
	words := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' || r == ' ' })
	if len(words) > n {
		return strings.Join(words[:n], " ") + more
	}
	return strings.Join(words, " ")
}

// StripShortcodes is strip_shortcodes(): registered shortcodes (with their
// content) are removed.
func StripShortcodes(text string, registry map[string]ShortcodeFunc) string {
	if !strings.Contains(text, "[") || len(registry) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '<' {
			i = elementEnd(text, i) - 1
			continue
		}
		if text[i] != '[' {
			continue
		}
		m, ok := matchShortcode(text, i, registry)
		if !ok {
			continue
		}
		b.WriteString(text[last:i])
		if m.escapedOpen && m.escapedClose {
			b.WriteString(text[i:m.end])
		} else {
			if m.escapedOpen {
				b.WriteByte('[')
			}
			if m.escapedClose {
				b.WriteByte(']')
			}
		}
		last = m.end
		i = m.end - 1
	}
	b.WriteString(text[last:])
	return b.String()
}
