package wpimport

import "strings"

// replaceInTags replaces old with new inside HTML tags only (not in text or
// comments' surroundings), like wp_replace_in_html_tags().
func replaceInTags(s, old, new string) string {
	if !strings.Contains(s, old) {
		return s
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.IndexByte(s[i:], '<')
		if j < 0 {
			break
		}
		start := i + j
		end := strings.IndexByte(s[start:], '>')
		if end < 0 {
			break
		}
		end += start + 1
		b.WriteString(s[i:start])
		b.WriteString(strings.ReplaceAll(s[start:end], old, new))
		i = end
	}
	b.WriteString(s[i:])
	return b.String()
}
