package wpimport

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/theinvisible/gopress/internal/render"
)

// shortcodeTags are the shortcodes registered on the source site; standalone
// ones are unwrapped from <p> after wpautop like shortcode_unautop() does.
var shortcodeTags = map[string]bool{
	"wp_caption": true, "caption": true, "gallery": true, "playlist": true, "audio": true,
	"video": true, "embed": true, "fotogalerie": true, "paypal-donation": true,
}

// dynamicBlocks converts dynamic (server-rendered) blocks into shortcodes
// handled by GoPress or its plugins.
var dynamicBlocks = map[string]func(attrs map[string]any) (string, bool){
	"foto-slider-lightbox/gallery": func(a map[string]any) (string, bool) {
		id, ok := a["galleryId"].(float64)
		if !ok {
			return "", false
		}
		return fmt.Sprintf(`[fotogalerie id="%d"]`, int64(id)), true
	},
	"core/more": func(a map[string]any) (string, bool) {
		if text, ok := a["customText"].(string); ok && text != "" {
			return "<!--more " + text + "-->", true
		}
		return "<!--more-->", true
	},
}

// convertContent turns stored WordPress post content into GoPress content:
// block delimiters are removed (static blocks keep their saved HTML), dynamic
// blocks become shortcodes and classic content gets wpautop baked in.
// It returns the new content and warnings.
func convertContent(content, homeURL string) (string, []string) {
	var warnings []string
	if strings.Contains(content, "<!-- wp:") {
		content, warnings = convertBlocks(content)
	} else {
		content = render.ShortcodeUnautop(render.Autop(content), shortcodeTags)
	}
	return relativizeURLs(content, homeURL), warnings
}

// convertBlocks strips block delimiters following the block grammar:
//
//	<!-- wp:name {json} -->inner<!-- /wp:name -->  or  <!-- wp:name {json} /-->
//
// "name" without a namespace means "core/name".
func convertBlocks(content string) (string, []string) {
	var b strings.Builder
	var warnings []string
	i := 0
	for {
		j := strings.Index(content[i:], "<!--")
		if j < 0 {
			break
		}
		start := i + j
		end := strings.Index(content[start:], "-->")
		if end < 0 {
			break
		}
		end += start + 3
		name, attrs, closing, selfClosing, ok := parseDelimiter(content[start:end])
		if !ok {
			b.WriteString(content[i:end])
			i = end
			continue
		}
		b.WriteString(content[i:start])
		i = end
		if closing {
			continue
		}
		if conv, isDynamic := dynamicBlocks[name]; isDynamic {
			if out, ok := conv(attrs); ok {
				b.WriteString(out)
			} else {
				warnings = append(warnings, fmt.Sprintf("block %s: cannot convert attrs %v", name, attrs))
			}
			if !selfClosing {
				// Skip the (ignored) inner content up to the closer.
				closer := "<!-- /wp:" + strings.TrimPrefix(name, "core/") + " -->"
				if k := strings.Index(content[i:], closer); k >= 0 {
					i += k + len(closer)
				}
			}
			continue
		}
		if selfClosing && !strings.HasPrefix(name, "core/") {
			warnings = append(warnings, "unknown dynamic block "+name+" dropped")
		}
	}
	b.WriteString(content[i:])
	return strings.TrimSpace(b.String()), warnings
}

// parseDelimiter parses a block comment such as `<!-- wp:image {"id":1} -->`.
func parseDelimiter(c string) (name string, attrs map[string]any, closing, selfClosing, ok bool) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(c, "<!--"), "-->"))
	if strings.HasPrefix(body, "/wp:") {
		closing = true
		body = body[1:]
	}
	if !strings.HasPrefix(body, "wp:") {
		return "", nil, false, false, false
	}
	body = body[3:]
	if strings.HasSuffix(body, "/") {
		selfClosing = true
		body = strings.TrimSpace(body[:len(body)-1])
	}
	name, rest, _ := strings.Cut(body, " ")
	if name == "" {
		return "", nil, false, false, false
	}
	if !strings.Contains(name, "/") {
		name = "core/" + name
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		if err := json.Unmarshal([]byte(rest), &attrs); err != nil {
			return "", nil, false, false, false
		}
	}
	return name, attrs, closing, selfClosing, true
}

// relativizeURLs makes same-site absolute URLs inside tags root-relative,
// so content works under any host. Link texts are left alone.
func relativizeURLs(content, homeURL string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(homeURL, "/"), "https://"), "http://")
	if host == "" {
		return content
	}
	for _, prefix := range []string{"https://" + host + "/", "http://" + host + "/"} {
		content = replaceInTags(content, `"`+prefix, `"/`)
		content = replaceInTags(content, `'`+prefix, `'/`)
	}
	return content
}
