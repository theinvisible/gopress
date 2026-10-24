package render

import (
	"regexp"
	"strings"
)

var reMoreTag = regexp.MustCompile(`<!--more(.*?)?-->`)

// SplitMore splits content at the first <!--more--> tag. It returns the
// teaser, the rest and the custom more text (from <!--more text-->).
func SplitMore(content string) (teaser, rest, moreText string, found bool) {
	loc := reMoreTag.FindStringSubmatchIndex(content)
	if loc == nil {
		return content, "", "", false
	}
	if loc[2] >= 0 {
		moreText = strings.TrimSpace(content[loc[2]:loc[3]])
	}
	return content[:loc[0]], content[loc[1]:], moreText, true
}

var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "source": true, "track": true, "wbr": true, "param": true,
}

// BalanceTags closes elements left open in s, a simplified
// force_balance_tags() for teasers cut at the more tag.
func BalanceTags(s string) string {
	var stack []string
	parts := htmlSplit(s)
	for i := 1; i < len(parts); i += 2 {
		el := parts[i]
		if strings.HasPrefix(el, "<!") || strings.HasPrefix(el, "<?") || strings.HasSuffix(el, "/>") {
			continue
		}
		closing := strings.HasPrefix(el, "</")
		name := strings.TrimLeft(el, "</")
		if j := strings.IndexAny(name, " \t\r\n/>"); j >= 0 {
			name = name[:j]
		}
		name = strings.ToLower(name)
		if name == "" || voidElements[name] {
			continue
		}
		if !closing {
			stack = append(stack, name)
			continue
		}
		for k := len(stack) - 1; k >= 0; k-- {
			if stack[k] == name {
				stack = stack[:k]
				break
			}
		}
	}
	for k := len(stack) - 1; k >= 0; k-- {
		s += "</" + stack[k] + ">"
	}
	return s
}
