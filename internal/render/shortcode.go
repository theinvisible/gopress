package render

import (
	"regexp"
	"strings"
)

// Shortcode is one parsed shortcode occurrence.
type Shortcode struct {
	Tag        string
	Attrs      map[string]string // named attributes, lower-cased keys
	Positional []string          // attributes without a name
	Content    string            // inner content of an enclosing shortcode
	Enclosing  bool
}

// Attr returns the named attribute or def.
func (s *Shortcode) Attr(name, def string) string {
	if v, ok := s.Attrs[name]; ok {
		return v
	}
	return def
}

// ShortcodeFunc renders a shortcode.
type ShortcodeFunc func(env *Env, sc *Shortcode) string

// DoShortcodes is a port of do_shortcode(): registered shortcodes in text
// are replaced by their output. Shortcodes inside HTML tags are left alone,
// unregistered ones are kept verbatim, and [[tag]] escapes to [tag].
func DoShortcodes(env *Env, text string, registry map[string]ShortcodeFunc) string {
	if !strings.Contains(text, "[") || len(registry) == 0 {
		return text
	}
	found := false
	for _, name := range shortcodeNamesIn(text) {
		if registry[name] != nil {
			found = true
			break
		}
	}
	if !found {
		return text
	}

	var b strings.Builder
	last := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '<':
			// Skip over elements; shortcodes in attributes are not expanded.
			i = elementEnd(text, i) - 1
			continue
		case '[':
		default:
			continue
		}
		m, ok := matchShortcode(text, i, registry)
		if !ok {
			continue
		}
		b.WriteString(text[last:i])
		if m.escapedOpen && m.escapedClose {
			b.WriteString(text[i+1 : m.end-1])
		} else {
			if m.escapedOpen {
				b.WriteByte('[')
			}
			b.WriteString(registry[m.sc.Tag](env, m.sc))
			if m.escapedClose {
				b.WriteByte(']')
			}
		}
		last = m.end
		i = m.end - 1
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

type shortcodeMatch struct {
	sc                        *Shortcode
	end                       int
	escapedOpen, escapedClose bool
}

// matchShortcode matches get_shortcode_regex() at text[i] == '['.
func matchShortcode(text string, i int, registry map[string]ShortcodeFunc) (shortcodeMatch, bool) {
	var m shortcodeMatch
	j := i + 1
	if j < len(text) && text[j] == '[' {
		m.escapedOpen = true
		j++
	}
	k := j
	for k < len(text) && isShortcodeNameChar(text[k]) {
		k++
	}
	name := text[j:k]
	if name == "" || registry[name] == nil {
		return m, false
	}
	close := strings.IndexByte(text[k:], ']')
	if close < 0 {
		return m, false
	}
	attrEnd := k + close // index of ']'
	sc := &Shortcode{Tag: name}
	end := attrEnd + 1
	attrText := text[k:attrEnd]
	if strings.HasSuffix(attrText, "/") {
		attrText = attrText[:len(attrText)-1]
	} else {
		closing := "[/" + name + "]"
		if c := strings.Index(text[end:], closing); c >= 0 {
			sc.Content = text[end : end+c]
			sc.Enclosing = true
			end += c + len(closing)
		}
	}
	if end < len(text) && text[end] == ']' {
		m.escapedClose = true
		end++
	}
	sc.Attrs, sc.Positional = ParseShortcodeAttrs(attrText)
	m.sc, m.end = sc, end
	return m, true
}

var reShortcodeAttr = regexp.MustCompile(`([\w-]+)\s*=\s*"([^"]*)"(?:\s|$)|([\w-]+)\s*=\s*'([^']*)'(?:\s|$)|([\w-]+)\s*=\s*([^\s'"]+)(?:\s|$)|"([^"]*)"(?:\s|$)|'([^']*)'(?:\s|$)|(\S+)(?:\s|$)`)

// ParseShortcodeAttrs is a port of shortcode_parse_atts().
func ParseShortcodeAttrs(text string) (map[string]string, []string) {
	attrs := map[string]string{}
	var positional []string
	text = strings.NewReplacer(" ", " ", "​", " ").Replace(text)
	for _, m := range reShortcodeAttr.FindAllStringSubmatch(text, -1) {
		switch {
		case m[1] != "":
			attrs[strings.ToLower(m[1])] = stripCSlashes(m[2])
		case m[3] != "":
			attrs[strings.ToLower(m[3])] = stripCSlashes(m[4])
		case m[5] != "":
			attrs[strings.ToLower(m[5])] = stripCSlashes(m[6])
		case m[7] != "":
			positional = append(positional, stripCSlashes(m[7]))
		case m[8] != "":
			positional = append(positional, stripCSlashes(m[8]))
		case m[9] != "":
			positional = append(positional, m[9])
		}
	}
	// Reject values with unclosed HTML elements.
	for k, v := range attrs {
		if strings.Contains(v, "<") && !balancedTags(v) {
			attrs[k] = ""
		}
	}
	return attrs, positional
}

// balancedTags reports whether v matches /^[^<]*+(?:<[^>]*+>[^<]*+)*+$/.
func balancedTags(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] == '<' {
			j := strings.IndexByte(v[i:], '>')
			if j < 0 {
				return false
			}
			i += j
		}
	}
	return true
}

func stripCSlashes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
