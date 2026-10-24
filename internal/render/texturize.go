package render

import (
	"strings"
)

// Texturize entities (en_US translations of the WordPress strings).
const (
	openingQuote       = "&#8220;"
	closingQuote       = "&#8221;"
	apos               = "&#8217;"
	prime              = "&#8242;"
	doublePrime        = "&#8243;"
	openingSingleQuote = "&#8216;"
	closingSingleQuote = "&#8217;"
	enDash             = "&#8211;"
	emDash             = "&#8212;"

	openQFlag  = "<!--oq-->"
	openSQFlag = "<!--osq-->"
	aposFlag   = "<!--apos-->"
)

var noTexturizeTags = map[string]bool{"pre": true, "code": true, "kbd": true, "style": true, "script": true, "tt": true}
var noTexturizeShortcodes = map[string]bool{"code": true}

var staticReplacer = func() []string {
	from := []string{"...", "``", "''", " (tm)",
		"'tain't", "'twere", "'twas", "'tis", "'twill", "'til", "'bout", "'nuff", "'round", "'cause", "'em"}
	to := []string{"&#8230;", openingQuote, closingQuote, " &#8482;",
		"&#8217;tain&#8217;t", "&#8217;twere", "&#8217;twas", "&#8217;tis", "&#8217;twill", "&#8217;til",
		"&#8217;bout", "&#8217;nuff", "&#8217;round", "&#8217;cause", "&#8217;em"}
	pairs := make([]string, 0, 2*len(from))
	for i := range from {
		pairs = append(pairs, from[i], to[i])
	}
	return pairs
}()

// Texturize is a port of wptexturize(): it replaces straight quotes, dashes,
// ellipses and similar with typographic entities, skipping HTML tags,
// comments, <pre>/<code>/... content and registered shortcodes.
func Texturize(text string, shortcodes map[string]bool) string {
	if text == "" {
		return text
	}
	var tagnames map[string]bool
	for _, name := range shortcodeNamesIn(text) {
		if shortcodes[name] {
			if tagnames == nil {
				tagnames = map[string]bool{}
			}
			tagnames[name] = true
		}
	}

	var tagStack, scStack []string
	parts := texturizeSplit(text, tagnames)
	for i, curl := range parts {
		if curl == "" {
			continue
		}
		switch {
		case curl[0] == '<':
			if strings.HasPrefix(curl, "<!--") {
				continue
			}
			curl = escapeAmpersands(curl)
			pushPopElement(curl, &tagStack, noTexturizeTags)
		case strings.Trim(curl, " \t\n\r\x00\x0B") == "":
			continue
		case curl[0] == '[' && tagnames != nil && isWholeShortcode(curl, tagnames):
			if !strings.HasPrefix(curl, "[[") && !strings.HasSuffix(curl, "]]") {
				pushPopElement(curl, &scStack, noTexturizeShortcodes)
			}
			continue
		case len(tagStack) == 0 && len(scStack) == 0:
			curl = texturizeText(curl)
		}
		parts[i] = curl
	}
	return strings.Join(parts, "")
}

func texturizeText(curl string) string {
	curl = strings.NewReplacer(staticReplacer...).Replace(curl)
	if strings.Contains(curl, "'") {
		curl = replaceDoubleDigitQuote(curl, '\'', closingSingleQuote)
		curl = replaceDoubleDigitQuote(curl, '"', closingQuote)
		curl = replaceYearApostrophe(curl)
		curl = replaceQuotedNumber(curl, '\'', openSQFlag, closingSingleQuote)
		curl = replaceOpeningQuote(curl, '\'', `([{"-`, openSQFlag, false)
		curl = replaceInWordApostrophe(curl)
		curl = texturizePrimes(curl, '\'', prime, openSQFlag, closingSingleQuote)
		curl = strings.ReplaceAll(curl, aposFlag, apos)
		curl = strings.ReplaceAll(curl, openSQFlag, openingSingleQuote)
	}
	if strings.Contains(curl, `"`) {
		curl = replaceQuotedNumber(curl, '"', openQFlag, closingQuote)
		curl = replaceOpeningQuote(curl, '"', `([{-`, openQFlag, true)
		curl = texturizePrimes(curl, '"', doublePrime, openQFlag, closingQuote)
		curl = strings.ReplaceAll(curl, openQFlag, openingQuote)
	}
	if strings.Contains(curl, "-") {
		curl = replaceDashes(curl)
	}
	if hasDigitXDigit(curl) {
		curl = replaceTimes(curl)
	}
	return escapeAmpersands(curl)
}

// --- context helpers (wp_spaces_regexp is `[\r\n\t ]|\xC2\xA0|&nbsp;`) ---

func spaceBefore(s string, i int) bool {
	if i > 0 {
		switch s[i-1] {
		case '\r', '\n', '\t', ' ':
			return true
		}
	}
	return strings.HasSuffix(s[:i], "\xC2\xA0") || strings.HasSuffix(s[:i], "&nbsp;")
}

func spaceAt(s string, i int) bool {
	if i < len(s) {
		switch s[i] {
		case '\r', '\n', '\t', ' ':
			return true
		}
	}
	return strings.HasPrefix(s[i:], "\xC2\xA0") || strings.HasPrefix(s[i:], "&nbsp;")
}

// endZ reports whether i is at \Z: the end, or before a final newline.
func endZ(s string, i int) bool {
	return i == len(s) || i == len(s)-1 && s[i] == '\n'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// closingContext is the lookahead (?=\Z|[.,:;!?)}\-\]]|&gt;|spaces).
func closingContext(s string, i int) bool {
	if endZ(s, i) {
		return true
	}
	if strings.IndexByte(".,:;!?)}-]", s[i]) >= 0 {
		return true
	}
	return strings.HasPrefix(s[i:], "&gt;") || spaceAt(s, i)
}

// --- apostrophe/quote patterns ---

// replaceDoubleDigitQuote: '(\d\d)X(?=closing) -> apos_flag $1 close, where X
// is the quote char q.
func replaceDoubleDigitQuote(s string, q byte, close string) string {
	var b strings.Builder
	last := 0
	for i := 0; i+3 < len(s); i++ {
		if s[i] == '\'' && isDigit(s[i+1]) && isDigit(s[i+2]) && s[i+3] == q && closingContext(s, i+4) {
			b.WriteString(s[last:i])
			b.WriteString(aposFlag)
			b.WriteString(s[i+1 : i+3])
			b.WriteString(close)
			last = i + 4
			i += 3
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// replaceYearApostrophe: '(?=\d\d(?:\Z|(?![%\d]|[.,]\d))) -> apos_flag.
func replaceYearApostrophe(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i+2 < len(s); i++ {
		if s[i] != '\'' || !isDigit(s[i+1]) || !isDigit(s[i+2]) {
			continue
		}
		j := i + 3
		ok := endZ(s, j)
		if !ok {
			ok = j >= len(s) || !(s[j] == '%' || isDigit(s[j]) || (s[j] == '.' || s[j] == ',') && j+1 < len(s) && isDigit(s[j+1]))
		}
		if ok {
			b.WriteString(s[last:i])
			b.WriteString(aposFlag)
			last = i + 1
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// replaceQuotedNumber: (?<=\A|spaces)Q(\d[.,\d]*)Q -> flag $1 close.
func replaceQuotedNumber(s string, q byte, flag, close string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != q || !(i == 0 || spaceBefore(s, i)) || i+1 >= len(s) || !isDigit(s[i+1]) {
			continue
		}
		j := i + 2
		for j < len(s) && (isDigit(s[j]) || s[j] == '.' || s[j] == ',') {
			j++
		}
		if j < len(s) && s[j] == q {
			b.WriteString(s[last:i])
			b.WriteString(flag)
			b.WriteString(s[i+1 : j])
			b.WriteString(close)
			last = j + 1
			i = j
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// replaceOpeningQuote: (?<=\A|[chars]|&lt;|spaces)Q -> flag. For double
// quotes the quote must not be followed by a space (?!spaces).
func replaceOpeningQuote(s string, q byte, chars, flag string, notBeforeSpace bool) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != q {
			continue
		}
		before := i == 0 || strings.IndexByte(chars, s[i-1]) >= 0 || strings.HasSuffix(s[:i], "&lt;") || spaceBefore(s, i)
		if !before || notBeforeSpace && spaceAt(s, i+1) {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(flag)
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// replaceInWordApostrophe:
// (?<!spaces)'(?!\Z|[.,:;!?"'(){}[\]\-]|&[lg]t;|spaces) -> apos_flag.
func replaceInWordApostrophe(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' || spaceBefore(s, i) {
			continue
		}
		j := i + 1
		if endZ(s, j) || strings.IndexByte(`.,:;!?"'(){}[]-`, s[j]) >= 0 ||
			strings.HasPrefix(s[j:], "&lt;") || strings.HasPrefix(s[j:], "&gt;") || spaceAt(s, j) {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(aposFlag)
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// texturizePrimes is a port of wptexturize_primes().
func texturizePrimes(haystack string, needle byte, primeStr, openQuote, closeQuote string) string {
	const flag = "<!--wp-prime-or-quote-->"
	n := string(needle)

	// quotePattern: needle(?=\Z|[.,:;!?)}\-\]]|&gt;|spaces) -> repl.
	quoteReplace := func(s, repl string) (string, int) {
		var b strings.Builder
		last, count := 0, 0
		for i := 0; i < len(s); i++ {
			if s[i] == needle && closingContext(s, i+1) {
				b.WriteString(s[last:i])
				b.WriteString(repl)
				last = i + 1
				count++
			}
		}
		if count == 0 {
			return s, 0
		}
		b.WriteString(s[last:])
		return b.String(), count
	}
	// replaceAfterDigit replaces tok preceded (or not) by a digit.
	replaceAfterDigit := func(s, tok, repl string, wantDigit bool) (string, int) {
		var b strings.Builder
		last, count := 0, 0
		for i := 0; i+len(tok) <= len(s); i++ {
			if !strings.HasPrefix(s[i:], tok) {
				continue
			}
			afterDigit := i > 0 && isDigit(s[i-1])
			if afterDigit != wantDigit {
				continue
			}
			b.WriteString(s[last:i])
			b.WriteString(repl)
			last = i + len(tok)
			i += len(tok) - 1
			count++
		}
		if count == 0 {
			return s, 0
		}
		b.WriteString(s[last:])
		return b.String(), count
	}

	sentences := strings.Split(haystack, openQuote)
	for key, sentence := range sentences {
		if !strings.Contains(sentence, n) {
			continue
		}
		if key != 0 && !strings.Contains(sentence, closeQuote) {
			var count int
			sentence, count = quoteReplace(sentence, flag)
			switch {
			case count > 1:
				var count2 int
				sentence, count2 = replaceAfterDigit(sentence, flag, closeQuote, false)
				if count2 == 0 {
					pos := strings.LastIndex(sentence, flag+".")
					if pos < 0 {
						pos = strings.LastIndex(sentence, flag)
					}
					sentence = sentence[:pos] + closeQuote + sentence[pos+len(flag):]
				}
				sentence, _ = replaceAfterDigit(sentence, n, primeStr, true)
				sentence, _ = replaceAfterDigit(sentence, flag, primeStr, true)
				sentence = strings.ReplaceAll(sentence, flag, closeQuote)
			case count == 1:
				sentence = strings.ReplaceAll(sentence, flag, closeQuote)
				sentence, _ = replaceAfterDigit(sentence, n, primeStr, true)
			default:
				sentence, _ = replaceAfterDigit(sentence, n, primeStr, true)
			}
		} else {
			sentence, _ = replaceAfterDigit(sentence, n, primeStr, true)
			sentence, _ = quoteReplace(sentence, closeQuote)
		}
		if needle == '"' && strings.Contains(sentence, `"`) {
			sentence = strings.ReplaceAll(sentence, `"`, closeQuote)
		}
		sentences[key] = sentence
	}
	return strings.Join(sentences, openQuote)
}

// --- dashes ---

// replaceDashes applies, in order: --- -> em dash; (?<=^|spaces)--(?=$|spaces)
// -> em dash; (?<!xn)-- -> en dash; (?<=^|spaces)-(?=$|spaces) -> en dash.
// ^ is the start, $ the end or before a final newline.
func replaceDashes(s string) string {
	s = strings.ReplaceAll(s, "---", emDash)
	s = replaceSpacedDash(s, "--", emDash)
	if strings.Contains(s, "--") {
		var b strings.Builder
		last := 0
		for i := 0; i+1 < len(s); i++ {
			if s[i] == '-' && s[i+1] == '-' && !strings.HasSuffix(s[:i], "xn") {
				b.WriteString(s[last:i])
				b.WriteString(enDash)
				last = i + 2
				i++
			}
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return replaceSpacedDash(s, "-", enDash)
}

func replaceSpacedDash(s, dash, repl string) string {
	var b strings.Builder
	last := 0
	for i := 0; i+len(dash) <= len(s); i++ {
		if !strings.HasPrefix(s[i:], dash) {
			continue
		}
		j := i + len(dash)
		if (i == 0 || spaceBefore(s, i)) && (endZ(s, j) || spaceAt(s, j)) {
			b.WriteString(s[last:i])
			b.WriteString(repl)
			last = j
			i = j - 1
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// --- 9x9 ---

func hasDigitXDigit(s string) bool {
	for i := 1; i+1 < len(s); i++ {
		if s[i] == 'x' && isDigit(s[i-1]) && isDigit(s[i+1]) {
			return true
		}
	}
	return false
}

func isNumChar(c byte) bool { return isDigit(c) || c == '.' || c == ',' }

// replaceTimes: \b(\d(?(?<=0)[\d\.,]+|[\d\.,]*))x(\d[\d\.,]*)\b -> $1&#215;$2.
func replaceTimes(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) || i > 0 && isWordChar(s[i-1]) {
			continue
		}
		j := i + 1
		for j < len(s) && isNumChar(s[j]) {
			j++
		}
		if s[i] == '0' && j == i+1 || j >= len(s) || s[j] != 'x' || j+1 >= len(s) || !isDigit(s[j+1]) {
			continue
		}
		k := j + 2
		for k < len(s) && isNumChar(s[k]) {
			k++
		}
		// Backtrack until a word boundary follows.
		for k > j+1 {
			lastWord := isWordChar(s[k-1])
			nextWord := k < len(s) && isWordChar(s[k])
			if lastWord != nextWord {
				break
			}
			k--
		}
		if k == j+1 {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(s[i:j])
		b.WriteString("&#215;")
		b.WriteString(s[j+1 : k])
		last = k
		i = k - 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// escapeAmpersands replaces & with &#038; unless it starts an entity:
// &(?!#(?:\d+|x[a-f0-9]+);|[a-z1-4]{1,8};) (case-insensitive).
func escapeAmpersands(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '&' || looksLikeEntity(s[i+1:]) {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString("&#038;")
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func looksLikeEntity(s string) bool {
	isHex := func(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
	if strings.HasPrefix(s, "#") {
		r := s[1:]
		if len(r) > 0 && (r[0] == 'x' || r[0] == 'X') {
			j := 1
			for j < len(r) && isHex(r[j]) {
				j++
			}
			if j > 1 && j < len(r) && r[j] == ';' {
				return true
			}
		}
		j := 0
		for j < len(r) && isDigit(r[j]) {
			j++
		}
		return j > 0 && j < len(r) && r[j] == ';'
	}
	j := 0
	for j < len(s) && j < 8 {
		c := s[j] | 0x20
		if !(c >= 'a' && c <= 'z' || s[j] >= '1' && s[j] <= '4') {
			break
		}
		j++
	}
	return j > 0 && j < len(s) && s[j] == ';'
}

// --- splitting ---

// shortcodeNamesIn returns the names matched by
// '@\[/?([^<>&/\[\]\x00-\x20=]++)@'.
func shortcodeNamesIn(text string) []string {
	var names []string
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		j := i + 1
		if j < len(text) && text[j] == '/' {
			j++
		}
		k := j
		for k < len(text) && !strings.ContainsRune("<>&/[]=", rune(text[k])) && text[k] > 0x20 {
			k++
		}
		if k > j {
			names = append(names, text[j:k])
		}
	}
	return names
}

// texturizeSplit splits text into text, HTML elements/comments and
// shortcodes of the given names (_get_wptexturize_split_regex()).
func texturizeSplit(text string, tagnames map[string]bool) []string {
	var parts []string
	start := 0
	for i := 0; i < len(text); {
		end := -1
		switch text[i] {
		case '<':
			if strings.HasPrefix(text[i:], "<!--") {
				if j := strings.Index(text[i+4:], "-->"); j >= 0 {
					end = i + 4 + j + 3
				} else {
					end = len(text)
				}
			} else if j := strings.IndexByte(text[i+1:], '>'); j >= 0 {
				end = i + 1 + j + 1
			} else {
				end = len(text)
			}
		case '[':
			if tagnames != nil {
				end = matchTexturizeShortcode(text, i, tagnames)
			}
		}
		if end < 0 {
			i++
			continue
		}
		parts = append(parts, text[start:i], text[i:end])
		start, i = end, end
	}
	return append(parts, text[start:])
}

// matchTexturizeShortcode matches
// \[[\/\[]?(?:names)(?=[\s\]\/])(?:[^\[\]<>]+|<[^\[\]>]*>)*+\]\]?
// at text[i] and returns the end offset, or -1.
func matchTexturizeShortcode(text string, i int, tagnames map[string]bool) int {
	j := i + 1
	if j < len(text) && (text[j] == '/' || text[j] == '[') {
		j++
	}
	matched := -1
	for name := range tagnames {
		if strings.HasPrefix(text[j:], name) {
			k := j + len(name)
			if k < len(text) && (isPCRESpace(text[k]) || text[k] == ']' || text[k] == '/') {
				matched = k
				break
			}
		}
	}
	if matched < 0 {
		return -1
	}
	k := matched
	for k < len(text) {
		c := text[k]
		if c == '<' {
			e := k + 1
			for e < len(text) && text[e] != '[' && text[e] != ']' && text[e] != '>' {
				e++
			}
			if e < len(text) && text[e] == '>' {
				k = e + 1
				continue
			}
			return -1 // possessive: no backtracking into the loop
		}
		if c == '[' || c == ']' || c == '>' {
			break
		}
		k++
	}
	if k >= len(text) || text[k] != ']' {
		return -1
	}
	k++
	if k < len(text) && text[k] == ']' {
		k++
	}
	return k
}

// isWholeShortcode reports whether curl entirely matches the shortcode regex.
func isWholeShortcode(curl string, tagnames map[string]bool) bool {
	return matchTexturizeShortcode(curl, 0, tagnames) == len(curl)
}

// pushPopElement is a port of _wptexturize_pushpop_element().
func pushPopElement(text string, stack *[]string, disabled map[string]bool) {
	offset := 1
	if len(text) > 1 && text[1] == '/' {
		if len(*stack) == 0 {
			return
		}
		offset = 2
	} else if len(text) <= 1 {
		if len(*stack) == 0 {
			return
		}
		offset = 2
	}
	opening := offset == 1
	var tag string
	if sp := strings.IndexByte(text, ' '); sp >= 0 {
		if sp >= offset {
			tag = text[offset:sp]
		}
	} else if len(text)-1 >= offset {
		tag = text[offset : len(text)-1]
	}
	if !disabled[tag] {
		return
	}
	if opening {
		*stack = append(*stack, tag)
	} else if (*stack)[len(*stack)-1] == tag {
		*stack = (*stack)[:len(*stack)-1]
	}
}
