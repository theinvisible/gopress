package render

import (
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var (
	commentPolicyOnce sync.Once
	commentPolicy     *bluemonday.Policy
)

// SanitizeComment restricts comment HTML to the tags WordPress allows in
// comments ($allowedtags): a, abbr, acronym, b, blockquote, cite, code,
// del, em, i, q, s, strike, strong (plus p and br from old imports).
// Links are forced to http(s)/mailto and get rel="nofollow ugc".
func SanitizeComment(s string) string {
	commentPolicyOnce.Do(func() {
		p := bluemonday.NewPolicy()
		p.AllowStandardURLs()
		p.AllowURLSchemes("http", "https", "mailto")
		p.AllowAttrs("href").OnElements("a")
		p.AllowAttrs("title").OnElements("a", "abbr", "acronym")
		p.AllowAttrs("cite").Matching(regexp.MustCompile(`^https?://`)).OnElements("blockquote", "q")
		p.AllowAttrs("datetime").OnElements("del")
		p.AllowElements("abbr", "acronym", "b", "blockquote", "cite", "code", "del", "em", "i", "q", "s", "strike", "strong", "p", "br")
		p.AllowAttrs("rel").Matching(regexp.MustCompile(`^(?:nofollow|ugc|external|noopener|noreferrer)(?: (?:nofollow|ugc|external|noopener|noreferrer))*$`)).OnElements("a")
		p.AddTargetBlankToFullyQualifiedLinks(false)
		p.RequireParseableURLs(true)
		commentPolicy = p
	})
	return addRelUGC(unescapeTextQuotes(commentPolicy.Sanitize(s)))
}

// addRelUGC makes sure every link carries rel="nofollow ugc"
// (wp_rel_ugc()). It works on parsed tokens of the sanitizer output and
// re-serializes only <a> start tags, so attribute values cannot break out.
func addRelUGC(s string) string {
	if !strings.Contains(s, "<a") {
		return s
	}
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			b.Write(z.Raw())
			continue
		}
		tok := z.Token()
		if tok.Data != "a" {
			b.WriteString(tok.String())
			continue
		}
		var rel []string
		attrs := tok.Attr[:0]
		for _, a := range tok.Attr {
			if a.Key == "rel" {
				rel = strings.Fields(a.Val)
				continue
			}
			attrs = append(attrs, a)
		}
		for _, need := range []string{"nofollow", "ugc"} {
			if !containsWord(rel, need) {
				rel = append(rel, need)
			}
		}
		tok.Attr = append(attrs, html.Attribute{Key: "rel", Val: strings.Join(rel, " ")})
		b.WriteString(tok.String())
	}
	return b.String()
}

func containsWord(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}

// unescapeTextQuotes undoes bluemonday's escaping of quotes in text nodes
// so that the output matches WordPress (and wptexturize can process it).
// Quotes are harmless in text; attribute values keep their escaping.
func unescapeTextQuotes(s string) string {
	if !strings.Contains(s, "&#") {
		return s
	}
	parts := htmlSplit(s)
	for i := 0; i < len(parts); i += 2 {
		parts[i] = strings.NewReplacer("&#39;", "'", "&#34;", `"`).Replace(parts[i])
	}
	return strings.Join(parts, "")
}

// SanitizeText strips all HTML (for author names and other plain fields).
func SanitizeText(s string) string {
	return bluemonday.StrictPolicy().Sanitize(s)
}

var (
	postPolicyOnce sync.Once
	postPolicy     *bluemonday.Policy
	reMoreComment  = regexp.MustCompile(`<!--(more(?:[^>]*?)|nextpage|noteaser)-->`)
)

// SanitizePost restricts post content written by users without the
// unfiltered_html capability (authors, contributors) to a safe subset of
// HTML, like wp_kses_post(): formatting, links, images, lists, tables,
// figures and the class/id/style attributes themes need, but no scripts,
// event handlers, forms, iframes or javascript: URLs. Shortcodes are plain
// text and survive; the more/nextpage comments are kept.
func SanitizePost(s string) string {
	postPolicyOnce.Do(func() {
		p := bluemonday.UGCPolicy()
		p.AllowURLSchemes("http", "https", "mailto", "tel")
		p.AllowAttrs("class", "id", "title", "lang", "dir").Globally()
		p.AllowAttrs("style").Globally()
		p.AllowStyles("text-align", "color", "background-color", "width", "height", "float", "margin", "padding",
			"font-weight", "font-style", "text-decoration").Globally()
		p.AllowAttrs("align").OnElements("p", "div", "img", "table", "td", "th", "h1", "h2", "h3", "h4", "h5", "h6")
		p.AllowAttrs("target").Matching(regexp.MustCompile(`^_blank$`)).OnElements("a")
		p.AllowAttrs("rel").Matching(regexp.MustCompile(`^[a-z ]+$`)).OnElements("a")
		p.AllowAttrs("srcset", "sizes", "loading", "decoding").OnElements("img")
		p.AllowElements("figure", "figcaption", "div", "span", "section", "mark", "small", "sub", "sup", "u", "s",
			"del", "ins", "kbd", "var", "samp", "abbr", "cite", "q", "dl", "dt", "dd", "hr", "pre", "code",
			"details", "summary", "caption", "colgroup", "col")
		p.AllowAttrs("data-id").Matching(regexp.MustCompile(`^\d+$`)).OnElements("img", "figure")
		p.RequireNoFollowOnLinks(false)
		p.AddTargetBlankToFullyQualifiedLinks(false)
		postPolicy = p
	})
	// bluemonday drops HTML comments; protect the ones WordPress uses
	// behind NUL-delimited placeholders (NUL is removed from the input).
	s = strings.ReplaceAll(s, "\x00", "")
	var kept []string
	s = reMoreComment.ReplaceAllStringFunc(s, func(m string) string {
		kept = append(kept, m)
		return "\x00GPKEEP" + strconv.Itoa(len(kept)-1) + "\x00"
	})
	out := unescapeTextQuotes(postPolicy.Sanitize(s))
	for i, m := range kept {
		out = strings.Replace(out, "\x00GPKEEP"+strconv.Itoa(i)+"\x00", m, 1)
	}
	return out
}
