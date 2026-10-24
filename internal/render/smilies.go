package render

import (
	"sort"
	"strings"
)

// smilies maps text smilies to emoji (WordPress' $wpsmiliestrans).
// ":mrgreen:" is an image in WordPress and is left alone here.
var smilies = map[string]string{
	":neutral:": "\U0001F610", ":twisted:": "\U0001F608", ":arrow:": "➡", ":shock:": "\U0001F62F",
	":smile:": "\U0001F642", ":???:": "\U0001F615", ":cool:": "\U0001F60E", ":evil:": "\U0001F47F",
	":grin:": "\U0001F600", ":idea:": "\U0001F4A1", ":oops:": "\U0001F633", ":razz:": "\U0001F61B",
	":roll:": "\U0001F644", ":wink:": "\U0001F609", ":cry:": "\U0001F625", ":eek:": "\U0001F62E",
	":lol:": "\U0001F606", ":mad:": "\U0001F621", ":sad:": "\U0001F641",
	"8-)": "\U0001F60E", "8-O": "\U0001F62F", ":-(": "\U0001F641", ":-)": "\U0001F642", ":-?": "\U0001F615",
	":-D": "\U0001F600", ":-P": "\U0001F61B", ":-o": "\U0001F62E", ":-x": "\U0001F621", ":-|": "\U0001F610",
	";-)": "\U0001F609", "8O": "\U0001F62F", ":(": "\U0001F641", ":)": "\U0001F642", ":?": "\U0001F615",
	":D": "\U0001F600", ":P": "\U0001F61B", ":o": "\U0001F62E", ":x": "\U0001F621", ":|": "\U0001F610",
	";)": "\U0001F609", ":!:": "❗", ":?:": "❓",
}

// smileyKeys are the smilies in krsort() order, which decides which
// alternative of WordPress' regex wins.
var smileyKeys = func() []string {
	keys := make([]string, 0, len(smilies))
	for k := range smilies {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys
}()

// ConvertSmilies is a port of convert_smilies(): text smilies surrounded by
// whitespace become emoji, outside of tags and code/pre/style/script/textarea.
func ConvertSmilies(text string) string {
	var b strings.Builder
	ignore := ""
	i := 0
	for i <= len(text) {
		// Split on <...> like preg_split('/(<[^>]*>)/U').
		lt := strings.IndexByte(text[i:], '<')
		var chunk, tag string
		if lt < 0 {
			chunk = text[i:]
			i = len(text) + 1
		} else {
			chunk = text[i : i+lt]
			gt := strings.IndexByte(text[i+lt:], '>')
			if gt < 0 {
				chunk = text[i:]
				i = len(text) + 1
			} else {
				tag = text[i+lt : i+lt+gt+1]
				i += lt + gt + 1
			}
		}
		if ignore == "" {
			chunk = smileText(chunk)
		}
		b.WriteString(chunk)
		if tag != "" {
			if ignore == "" {
				for _, t := range []string{"code", "pre", "style", "script", "textarea"} {
					if strings.HasPrefix(tag, "<"+t) {
						ignore = t
						break
					}
				}
			}
			b.WriteString(tag)
			if ignore != "" && tag == "</"+ignore+">" {
				ignore = ""
			}
		}
	}
	return b.String()
}

func smileText(s string) string {
	if !strings.ContainsAny(s, ":;8") {
		return s
	}
	var b strings.Builder
	last := 0
	for p := 0; p < len(s); p++ {
		if p > 0 && !spaceBefore(s, p) && s[p-1] != '\n' {
			continue
		}
		for _, k := range smileyKeys {
			if !strings.HasPrefix(s[p:], k) {
				continue
			}
			e := p + len(k)
			if e == len(s) || s[e] == '\n' || spaceAt(s, e) {
				b.WriteString(s[last:p])
				b.WriteString(smilies[k])
				last = e
				p = e - 1
				break
			}
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// CapitalPDangit is a port of capital_P_dangit() for content (not titles).
var CapitalPDangit = strings.NewReplacer(
	" Wordpress", " WordPress", "&#8216;Wordpress", "&#8216;WordPress",
	"&#8220;Wordpress", "&#8220;WordPress", ">Wordpress", ">WordPress", "(Wordpress", "(WordPress",
).Replace
