package render

import (
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
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

var reAnchor = regexp.MustCompile(`<a\s[^>]*>`)

// addRelUGC makes sure every link in user content carries
// rel="nofollow ugc" (wp_rel_ugc()).
func addRelUGC(s string) string {
	return reAnchor.ReplaceAllStringFunc(s, func(tag string) string {
		if m := reRelAttr.FindStringSubmatchIndex(tag); m != nil {
			rel := strings.Fields(tag[m[2]:m[3]])
			for _, need := range []string{"nofollow", "ugc"} {
				if !containsWord(rel, need) {
					rel = append(rel, need)
				}
			}
			return tag[:m[2]] + strings.Join(rel, " ") + tag[m[3]:]
		}
		return strings.TrimSuffix(tag, ">") + ` rel="nofollow ugc">`
	})
}

var reRelAttr = regexp.MustCompile(`\srel="([^"]*)"`)

func containsWord(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}

// unescapeTextQuotes undoes bluemonday's escaping of quotes in text so
// that wptexturize can turn them into typographic quotes. Attribute values
// are double-quoted, so &#34; stays escaped inside tags.
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
