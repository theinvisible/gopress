package render

import (
	"regexp"
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
		p.RequireNoFollowOnLinks(true)
		p.AddTargetBlankToFullyQualifiedLinks(false)
		p.RequireParseableURLs(true)
		commentPolicy = p
	})
	return commentPolicy.Sanitize(s)
}

// SanitizeText strips all HTML (for author names and other plain fields).
func SanitizeText(s string) string {
	return bluemonday.StrictPolicy().Sanitize(s)
}
