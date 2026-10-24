package render

import (
	"regexp"
	"strconv"
	"strings"
)

const allBlocks = `(?:table|thead|tfoot|caption|col|colgroup|tbody|tr|td|th|div|dl|dd|dt|ul|ol|li|pre|form|map|area|blockquote|address|style|p|h[1-6]|hr|fieldset|legend|section|article|aside|hgroup|header|footer|nav|figure|figcaption|details|menu|summary)`

var (
	reDoubleBr        = regexp.MustCompile(`<br\s*/?>\s*<br\s*/?>`)
	reBlockOpen       = regexp.MustCompile(`(<` + allBlocks + `[\s/>])`)
	reBlockClose      = regexp.MustCompile(`(</` + allBlocks + `>)`)
	reHr              = regexp.MustCompile(`(<hr\s*?/?>)`)
	reOptionBefore    = regexp.MustCompile(`\s*<option`)
	reOptionAfter     = regexp.MustCompile(`</option>\s*`)
	reObjectOpen      = regexp.MustCompile(`(<object[^>]*>)\s*`)
	reObjectClose     = regexp.MustCompile(`\s*</object>`)
	reParamEmbed      = regexp.MustCompile(`\s*(</?(?:param|embed)[^>]*>)\s*`)
	reMediaOpen       = regexp.MustCompile(`([<\[](?:audio|video)[^>\]]*[>\]])\s*`)
	reMediaClose      = regexp.MustCompile(`\s*([<\[]/(?:audio|video)[>\]])`)
	reSourceTrack     = regexp.MustCompile(`\s*(<(?:source|track)[^>]*>)\s*`)
	reFigcaptionOpen  = regexp.MustCompile(`\s*(<figcaption[^>]*>)`)
	reFigcaptionClose = regexp.MustCompile(`</figcaption>\s*`)
	reMultiNL         = regexp.MustCompile(`\n\n+`)
	reParaSplit       = regexp.MustCompile(`\n\s*\n`)
	reEmptyP          = regexp.MustCompile(`<p>\s*</p>`)
	reUnclosedP       = regexp.MustCompile(`<p>([^<]+)</(div|address|form)>`)
	reWrappedBlock    = regexp.MustCompile(`<p>\s*(</?` + allBlocks + `[^>]*>)\s*</p>`)
	reWrappedLi       = regexp.MustCompile(`<p>(<li.+?)</p>`)
	reBlockquoteP     = regexp.MustCompile(`(?i)<p><blockquote((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	rePBeforeBlock    = regexp.MustCompile(`<p>\s*(</?` + allBlocks + `[^>]*>)`)
	rePAfterBlock     = regexp.MustCompile(`(</?` + allBlocks + `[^>]*>)\s*</p>`)
	reBrAfterBlock    = regexp.MustCompile(`(</?` + allBlocks + `[^>]*>)\s*<br />`)
	reBrBeforeBlock   = regexp.MustCompile(`<br />(\s*</?(?:p|li|div|dl|dd|dt|th|pre|td|ul|ol)[^>]*>)`)
)

// Autop is a port of wpautop($text, true): it turns double line breaks into
// paragraphs and remaining single line breaks into <br />.
func Autop(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	text += "\n"

	// Pre tags are not touched; swap them for placeholders.
	var preNames, preTags []string
	if strings.Contains(text, "<pre") {
		parts := strings.Split(text, "</pre>")
		last := parts[len(parts)-1]
		var b strings.Builder
		for _, part := range parts[:len(parts)-1] {
			start := strings.Index(part, "<pre")
			if start < 0 {
				b.WriteString(part)
				continue
			}
			name := "<pre wp-pre-tag-" + strconv.Itoa(len(preNames)) + "></pre>"
			preNames = append(preNames, name)
			preTags = append(preTags, part[start:]+"</pre>")
			b.WriteString(part[:start])
			b.WriteString(name)
		}
		b.WriteString(last)
		text = b.String()
	}

	text = reDoubleBr.ReplaceAllString(text, "\n\n")
	text = reBlockOpen.ReplaceAllString(text, "\n\n$1")
	text = reBlockClose.ReplaceAllString(text, "$1\n\n")
	text = reHr.ReplaceAllString(text, "$1\n\n")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = replaceInHTMLTags(text, "\n", " <!-- wpnl --> ")

	if strings.Contains(text, "<option") {
		text = reOptionBefore.ReplaceAllString(text, "<option")
		text = reOptionAfter.ReplaceAllString(text, "</option>")
	}
	if strings.Contains(text, "</object>") {
		text = reObjectOpen.ReplaceAllString(text, "$1")
		text = reObjectClose.ReplaceAllString(text, "</object>")
		text = reParamEmbed.ReplaceAllString(text, "$1")
	}
	if strings.Contains(text, "<source") || strings.Contains(text, "<track") {
		text = reMediaOpen.ReplaceAllString(text, "$1")
		text = reMediaClose.ReplaceAllString(text, "$1")
		text = reSourceTrack.ReplaceAllString(text, "$1")
	}
	if strings.Contains(text, "<figcaption") {
		text = reFigcaptionOpen.ReplaceAllString(text, "$1")
		text = reFigcaptionClose.ReplaceAllString(text, "</figcaption>")
	}

	text = reMultiNL.ReplaceAllString(text, "\n\n")

	var b strings.Builder
	for _, p := range reParaSplit.Split(text, -1) {
		if p == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.Trim(p, "\n"))
		b.WriteString("</p>\n")
	}
	text = b.String()

	text = reEmptyP.ReplaceAllString(text, "")
	text = reUnclosedP.ReplaceAllString(text, "<p>$1</p></$2>")
	text = reWrappedBlock.ReplaceAllString(text, "$1")
	text = reWrappedLi.ReplaceAllString(text, "$1")
	text = reBlockquoteP.ReplaceAllString(text, "<blockquote$1><p>")
	text = strings.ReplaceAll(text, "</blockquote></p>", "</p></blockquote>")
	text = rePBeforeBlock.ReplaceAllString(text, "$1")
	text = rePAfterBlock.ReplaceAllString(text, "$1")

	// Line breaks.
	text = preserveNewlines(text)
	text = strings.ReplaceAll(text, "<br>", "<br />")
	text = strings.ReplaceAll(text, "<br/>", "<br />")
	text = newlinesToBr(text)
	text = strings.ReplaceAll(text, "<WPPreserveNewline />", "\n")

	text = reBrAfterBlock.ReplaceAllString(text, "$1")
	text = reBrBeforeBlock.ReplaceAllString(text, "$1")
	if strings.HasSuffix(text, "\n</p>") {
		text = text[:len(text)-len("\n</p>")] + "</p>"
	}

	for i, name := range preNames {
		text = strings.Replace(text, name, preTags[i], 1)
	}
	if strings.Contains(text, "<!-- wpnl -->") {
		text = strings.ReplaceAll(text, " <!-- wpnl --> ", "\n")
		text = strings.ReplaceAll(text, "<!-- wpnl -->", "\n")
	}
	return text
}

// preserveNewlines replaces newlines inside <script>, <style>, <svg> and
// <math> elements with a placeholder (preg_replace_callback with
// '/<(script|style|svg|math).*?<\/\1>/s').
func preserveNewlines(text string) string {
	var b strings.Builder
	i := 0
	for {
		start, name := -1, ""
		for _, tag := range []string{"script", "style", "svg", "math"} {
			if j := strings.Index(text[i:], "<"+tag); j >= 0 && (start < 0 || i+j < start) {
				start, name = i+j, tag
			}
		}
		if start < 0 {
			break
		}
		closeTag := "</" + name + ">"
		end := strings.Index(text[start+1+len(name):], closeTag)
		if end < 0 {
			// No closing tag: no match at this position; continue after it.
			b.WriteString(text[i : start+1])
			i = start + 1
			continue
		}
		end += start + 1 + len(name) + len(closeTag)
		b.WriteString(text[i:start])
		b.WriteString(strings.ReplaceAll(text[start:end], "\n", "<WPPreserveNewline />"))
		i = end
	}
	if i == 0 {
		return text
	}
	b.WriteString(text[i:])
	return b.String()
}

// newlinesToBr implements preg_replace('|(?<!<br />)\s*\n|', "<br />\n").
// The regex is tried at each position from the left; from a start that is
// not preceded by "<br />", \s* greedily takes the whitespace run up to its
// last newline, and the whole match becomes one "<br />\n".
func newlinesToBr(text string) string {
	var b strings.Builder
	b.Grow(len(text) + len(text)/8)
	last := 0
	for p := 0; p < len(text); p++ {
		if !isPCRESpace(text[p]) || strings.HasSuffix(text[:p], "<br />") {
			continue
		}
		r := p
		for r < len(text) && isPCRESpace(text[r]) {
			r++
		}
		nl := strings.LastIndexByte(text[p:r], '\n')
		if nl < 0 {
			p = r - 1
			continue
		}
		b.WriteString(text[last:p])
		b.WriteString("<br />\n")
		last = p + nl + 1
		p = last - 1
	}
	b.WriteString(text[last:])
	return b.String()
}

func isPCRESpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// ShortcodeUnautop removes <p> wrappers around standalone shortcodes whose
// names are in tags, like shortcode_unautop().
func ShortcodeUnautop(text string, tags map[string]bool) string {
	if len(tags) == 0 || !strings.Contains(text, "<p>") {
		return text
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(text[i:], "<p>")
		if j < 0 {
			break
		}
		p := i + j
		if sc, end, ok := standaloneShortcode(text, p+3, tags); ok {
			b.WriteString(text[i:p])
			b.WriteString(sc)
			i = end
			continue
		}
		b.WriteString(text[i : p+3])
		i = p + 3
	}
	b.WriteString(text[i:])
	return b.String()
}

// standaloneShortcode checks whether text[pos:] is optional spaces, one
// registered shortcode (self-closing or enclosing), optional spaces and
// "</p>". It returns the shortcode text and the offset after "</p>".
func standaloneShortcode(text string, pos int, tags map[string]bool) (string, int, bool) {
	pos = skipWPSpaces(text, pos)
	if pos >= len(text) || text[pos] != '[' {
		return "", 0, false
	}
	nameEnd := pos + 1
	for nameEnd < len(text) && isShortcodeNameChar(text[nameEnd]) {
		nameEnd++
	}
	name := text[pos+1 : nameEnd]
	if !tags[name] {
		return "", 0, false
	}
	if nameEnd < len(text) && (isWordChar(text[nameEnd]) || text[nameEnd] == '-') {
		return "", 0, false
	}
	close := strings.IndexByte(text[nameEnd:], ']')
	if close < 0 {
		return "", 0, false
	}
	openEnd := nameEnd + close + 1 // after ']'
	selfClosing := text[openEnd-2] == '/'

	tryEnd := func(scEnd int) (string, int, bool) {
		e := skipWPSpaces(text, scEnd)
		if strings.HasPrefix(text[e:], "</p>") {
			return text[pos:scEnd], e + 4, true
		}
		return "", 0, false
	}
	if !selfClosing {
		closing := "[/" + name + "]"
		if k := strings.Index(text[openEnd:], closing); k >= 0 {
			if sc, end, ok := tryEnd(openEnd + k + len(closing)); ok {
				return sc, end, true
			}
		}
	}
	return tryEnd(openEnd)
}

func skipWPSpaces(text string, pos int) int {
	for pos < len(text) {
		switch {
		case text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\r' || text[pos] == '\n':
			pos++
		case strings.HasPrefix(text[pos:], "\xC2\xA0"):
			pos += 2
		case strings.HasPrefix(text[pos:], "&nbsp;"):
			pos += 6
		default:
			return pos
		}
	}
	return pos
}

func isWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isShortcodeNameChar(c byte) bool {
	return isWordChar(c) || c == '-'
}
