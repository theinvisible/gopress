package render

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

const urlChar = `[\w#%~/@\[\]*(+=&$\-\x{80}-\x{10FFFF}]`

var (
	reClickURL   = regexp.MustCompile(`([\s(<.,;:!?])(\w{1,20}://` + urlChar + `*(?:['.,;:!?)]` + urlChar + `+)*)(\)?)(\.\w{2,6})?`)
	reClickWWW   = regexp.MustCompile(`(?is)([\s>])((?:www|ftp)\.[\w\x{80}-\x{10FFFF}#$%&~/.\-;:=,?@\[\]+]+)`)
	reClickEmail = regexp.MustCompile(`(?i)([\s>])([.0-9a-z_+-]+)@((?:[0-9a-z-]+\.)+[0-9a-z]{2,})`)
	reNestedLink = regexp.MustCompile(`(?i)(<a(?:[ \r\n\t]+[^>]+?>|>))<a [^>]+?>([^>]+?)</a></a>`)
	reHTMLTag    = regexp.MustCompile(`<[^<>]+>`)
	reSchemeTag  = regexp.MustCompile(`^<\s*\w{1,20}://`)
)

// MakeClickable is a port of make_clickable(): bare URLs, www./ftp. hosts
// and email addresses in text become links. Text inside <code>, <pre>,
// <script> and <style> is left alone. host is the site's host; links to
// other hosts get rel="nofollow".
func MakeClickable(text, host string) string {
	var b strings.Builder
	nested := 0
	last := 0
	pieces := []string{}
	for _, loc := range reHTMLTag.FindAllStringIndex(text, -1) {
		pieces = append(pieces, text[last:loc[0]], text[loc[0]:loc[1]])
		last = loc[1]
	}
	pieces = append(pieces, text[last:])
	for _, piece := range pieces {
		lower := strings.ToLower(piece)
		switch {
		case hasTagPrefix(lower, "code"), hasTagPrefix(lower, "pre"), hasTagPrefix(lower, "script"), hasTagPrefix(lower, "style"):
			nested++
		case nested > 0 && (lower == "</code>" || lower == "</pre>" || lower == "</script>" || lower == "</style>"):
			nested--
		}
		if nested > 0 || piece == "" || piece[0] == '<' && !reSchemeTag.MatchString(piece) {
			b.WriteString(piece)
			continue
		}
		ret := " " + piece + " "
		ret = reClickURL.ReplaceAllStringFunc(ret, func(m string) string {
			sub := reClickURL.FindStringSubmatch(m)
			u, suffix := sub[2], sub[3]
			if sub[3] == ")" && strings.Contains(u, "(") {
				u += sub[3]
				suffix = ""
			}
			u += sub[4]
			for strings.Count(u, "(") < strings.Count(u, ")") {
				i := strings.LastIndexByte(u, ')')
				suffix = u[i:] + suffix
				u = u[:i]
			}
			safe := EscURL(u)
			if safe == "" {
				return m
			}
			return sub[1] + `<a href="` + safe + `"` + relAttr(safe, host) + `>` + safe + `</a>` + suffix
		})
		ret = reClickWWW.ReplaceAllStringFunc(ret, func(m string) string {
			sub := reClickWWW.FindStringSubmatch(m)
			dest, tail := "http://"+sub[2], ""
			if c := dest[len(dest)-1]; strings.IndexByte(".,;:)", c) >= 0 {
				tail = string(c)
				dest = dest[:len(dest)-1]
			}
			safe := EscURL(dest)
			if safe == "" {
				return m
			}
			return sub[1] + `<a href="` + safe + `"` + relAttr(safe, host) + `>` + safe + `</a>` + tail
		})
		ret = reClickEmail.ReplaceAllString(ret, `$1<a href="mailto:$2@$3">$2@$3</a>`)
		b.WriteString(ret[1 : len(ret)-1])
	}
	return reNestedLink.ReplaceAllString(b.String(), "$1$2</a>")
}

func hasTagPrefix(lower, name string) bool {
	if !strings.HasPrefix(lower, "<"+name) || len(lower) <= len(name)+1 {
		return false
	}
	c := lower[len(name)+1]
	return c == '>' || c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func relAttr(u, host string) string {
	if pu, err := url.Parse(html.UnescapeString(u)); err == nil && pu.Host != "" && !strings.EqualFold(pu.Host, host) {
		return ` rel="nofollow"`
	}
	return ""
}

// EscURL is a simplified esc_url(): only http, https, ftp, mailto and
// relative URLs are allowed; the result is attribute-safe.
func EscURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	u = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, u)
	u = strings.ReplaceAll(u, " ", "%20")
	if i := strings.IndexAny(u, ":/?#"); i > 0 && u[i] == ':' {
		switch strings.ToLower(u[:i]) {
		case "http", "https", "ftp", "ftps", "mailto", "news", "irc", "irc6", "ircs", "gopher", "nntp", "feed", "telnet", "mms", "rtsp", "sms", "svn", "tel", "fax", "xmpp", "webcal", "urn":
		default:
			return ""
		}
	}
	return strings.NewReplacer(`"`, "&quot;", "'", "&#039;", "<", "%3C", ">", "%3E", "&", "&#038;").Replace(html.UnescapeString(u))
}
