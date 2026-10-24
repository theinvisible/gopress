package wpimport

import (
	"encoding/json"
	"fmt"
	"regexp"
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

// convertBlocks renders block content the way do_blocks() does for the
// blocks used on the source site, so the stored HTML needs no block
// rendering later. The block grammar is
//
//	<!-- wp:name {json} -->inner<!-- /wp:name -->  or  <!-- wp:name {json} /-->
//
// where "name" without a namespace means "core/name". Static blocks keep
// their saved HTML plus the classes WordPress adds at render time (layout
// classes, wp-block-paragraph, ...); dynamic blocks become shortcodes.
func convertBlocks(content string) (string, []string) {
	root, warnings := parseBlocks(content)
	r := blockRenderer{}
	out := r.renderChildren(root)
	return strings.TrimSpace(out), append(warnings, r.warnings...)
}

type blockNode struct {
	text     string // for text nodes (name == "")
	name     string
	attrs    map[string]any
	children []*blockNode
}

// parseBlocks builds the block tree. Unbalanced delimiters are treated as
// text.
func parseBlocks(content string) (*blockNode, []string) {
	root := &blockNode{name: "root"}
	stack := []*blockNode{root}
	var warnings []string
	top := func() *blockNode { return stack[len(stack)-1] }
	addText := func(t string) {
		if t != "" {
			top().children = append(top().children, &blockNode{text: t})
		}
	}
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
			addText(content[i:end])
			i = end
			continue
		}
		addText(content[i:start])
		i = end
		switch {
		case closing:
			if len(stack) > 1 && top().name == name {
				stack = stack[:len(stack)-1]
			} else {
				warnings = append(warnings, "unbalanced closer for "+name)
			}
		case selfClosing:
			top().children = append(top().children, &blockNode{name: name, attrs: attrs})
		default:
			b := &blockNode{name: name, attrs: attrs}
			top().children = append(top().children, b)
			stack = append(stack, b)
		}
	}
	addText(content[i:])
	if len(stack) > 1 {
		warnings = append(warnings, "unclosed block "+top().name)
	}
	return root, warnings
}

type blockRenderer struct {
	galleries int
	warnings  []string
}

func (r *blockRenderer) renderChildren(b *blockNode) string {
	var sb strings.Builder
	for _, c := range b.children {
		if c.name == "" {
			sb.WriteString(c.text)
		} else {
			sb.WriteString(r.render(c))
		}
	}
	return sb.String()
}

func (r *blockRenderer) render(b *blockNode) string {
	if conv, ok := dynamicBlocks[b.name]; ok {
		out, ok := conv(b.attrs)
		if !ok {
			r.warnings = append(r.warnings, fmt.Sprintf("block %s: cannot convert attrs %v", b.name, b.attrs))
		}
		return out
	}
	inner := r.renderChildren(b)
	if !strings.HasPrefix(b.name, "core/") {
		r.warnings = append(r.warnings, "unknown block "+b.name+" rendered as saved HTML")
		return inner
	}
	switch b.name {
	case "core/paragraph":
		return addClassToFirstTag(inner, "wp-block-paragraph")
	case "core/image":
		return restoreImageOuterContainer(inner, b.attrs)
	case "core/list":
		if !strings.Contains(firstTag(inner), "wp-block-list") {
			return addClassToFirstTag(inner, "wp-block-list")
		}
	case "core/quote":
		return addClassToFirstTag(inner, "is-layout-flow wp-block-quote-is-layout-flow")
	case "core/column":
		return addClassToFirstTag(inner, "is-layout-flow wp-block-column-is-layout-flow")
	case "core/columns":
		return addClassToFirstTag(inner, "is-layout-flex wp-block-columns-is-layout-flex")
	case "core/gallery":
		r.galleries++
		inner = addClassToFirstTag(inner, fmt.Sprintf("wp-block-gallery-%d is-layout-flex wp-block-gallery-is-layout-flex", r.galleries))
		return addImageDataIDs(inner)
	case "core/group":
		layout := "flow"
		if l, ok := b.attrs["layout"].(map[string]any); ok {
			if t, ok := l["type"].(string); ok && t != "default" {
				layout = t
			}
		}
		// Classic themes get the inner container wrapper
		// (wp_restore_group_inner_container).
		open := firstTagEnd(inner)
		closeAt := strings.LastIndex(inner, "</")
		if open < 0 || closeAt < open {
			return inner
		}
		return inner[:open] + `<div class="wp-block-group__inner-container is-layout-` + layout +
			` wp-block-group-is-layout-` + layout + `">` + inner[open:closeAt] + "</div>" + inner[closeAt:]
	}
	return inner
}

// restoreImageOuterContainer is wp_restore_image_outer_container(): in
// classic themes, aligned images get a <div class="wp-block-image"> wrapper
// that takes over the wp-block-image class.
func restoreImageOuterContainer(inner string, attrs map[string]any) string {
	tag := firstTag(inner)
	if !strings.HasPrefix(tag, "<figure") {
		return inner
	}
	m := reClassAttr.FindStringSubmatchIndex(tag)
	if m == nil {
		return inner
	}
	classes := strings.Fields(tag[m[2]:m[3]])
	has := func(c string) bool {
		for _, x := range classes {
			if x == c {
				return true
			}
		}
		return false
	}
	if !has("wp-block-image") || !(has("alignleft") || has("aligncenter") || has("alignright")) {
		return inner
	}
	wrapper := []string{"wp-block-image"}
	if cn, ok := attrs["className"].(string); ok {
		wrapper = append(wrapper, strings.Fields(cn)...)
	}
	var kept []string
	for _, c := range classes {
		moved := false
		for _, w := range wrapper {
			if c == w {
				moved = true
			}
		}
		if !moved {
			kept = append(kept, c)
		}
	}
	newTag := tag[:m[2]] + strings.Join(kept, " ") + tag[m[3]:]
	return `<div class="` + strings.Join(wrapper, " ") + `">` + strings.Replace(inner, tag, newTag, 1) + "</div>"
}

// firstTag returns the first start tag in s.
func firstTag(s string) string {
	i := strings.IndexByte(s, '<')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return ""
	}
	return s[i : i+j+1]
}

// firstTagEnd returns the offset after the first start tag, or -1.
func firstTagEnd(s string) int {
	i := strings.IndexByte(s, '<')
	if i < 0 {
		return -1
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return -1
	}
	return i + j + 1
}

var reClassAttr = regexp.MustCompile(`\sclass="([^"]*)"`)

// addClassToFirstTag appends classes to the class attribute of the first
// start tag in s, adding the attribute if needed.
func addClassToFirstTag(s, classes string) string {
	tag := firstTag(s)
	if tag == "" {
		return s
	}
	var newTag string
	if m := reClassAttr.FindStringSubmatchIndex(tag); m != nil {
		existing := tag[m[2]:m[3]]
		joined := strings.TrimSpace(existing + " " + classes)
		newTag = tag[:m[2]] + joined + tag[m[3]:]
	} else {
		name := tag[1:]
		k := strings.IndexAny(name, " \t\n/>")
		if k < 0 {
			return s
		}
		newTag = "<" + name[:k] + ` class="` + classes + `"` + name[k:]
	}
	return strings.Replace(s, tag, newTag, 1)
}

var reWPImageClass = regexp.MustCompile(`<img ([^>]*class="[^"]*wp-image-(\d+)[^"]*"[^>]*)>`)

// addImageDataIDs adds data-id="N" to gallery images, as the gallery block
// renderer does.
func addImageDataIDs(s string) string {
	return reWPImageClass.ReplaceAllStringFunc(s, func(tag string) string {
		if strings.Contains(tag, "data-id=") {
			return tag
		}
		m := reWPImageClass.FindStringSubmatch(tag)
		return `<img data-id="` + m[2] + `" ` + m[1] + `>`
	})
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
