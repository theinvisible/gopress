package render

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// imageSizeLimits are the configured intermediate sizes (width, height, crop).
var imageSizeLimits = map[string][2]int{
	"thumbnail": {150, 150}, "medium": {300, 300}, "medium_large": {768, 0}, "large": {1024, 1024},
	"post-thumbnail": {624, 9999},
}

// attachmentImageSrc is image_downsize(): the file and dimensions of an
// attachment in the requested size, falling back to the full image.
func attachmentImageSrc(m *db.Media, size string) (file string, w, h int) {
	dir := ""
	if i := strings.LastIndexByte(m.File, '/'); i >= 0 {
		dir = m.File[:i+1]
	}
	if s, ok := m.Sizes[size]; ok && size != "full" {
		return dir + s.File, s.Width, s.Height
	}
	w, h = m.Width, m.Height
	if lim, ok := imageSizeLimits[size]; ok && size != "full" {
		w, h = constrainDimensions(w, h, lim[0], lim[1])
	}
	return m.File, w, h
}

// AttachmentImage is wp_get_attachment_image() for image attachments.
// extra attributes are appended after alt (e.g. aria-describedby).
func AttachmentImage(env *Env, att *db.Post, m *db.Media, size string, extra map[string]string) string {
	file, w, h := attachmentImageSrc(m, size)
	if file == "" {
		return ""
	}
	src := env.uploadURL(file)
	var b strings.Builder
	fmt.Fprintf(&b, `<img decoding="async" width="%d" height="%d" src="%s" class="attachment-%s size-%s" alt="%s"`,
		w, h, html.EscapeString(src), size, size, html.EscapeString(m.Alt))
	for _, k := range sortedKeys(extra) {
		fmt.Fprintf(&b, ` %s="%s"`, k, html.EscapeString(extra[k]))
	}
	if srcset := calculateSrcset(env, w, h, src, m); srcset != "" {
		fmt.Fprintf(&b, ` srcset="%s" sizes="(max-width: %dpx) 100vw, %dpx"`, srcset, w, w)
	}
	b.WriteString(" />")
	return b.String()
}

// AttachmentLink is wp_get_attachment_link(): the image linked to its
// attachment page (permalink=true) or to the file.
func AttachmentLink(env *Env, att *db.Post, m *db.Media, size string, permalink bool, extra map[string]string) string {
	href := env.uploadURL(m.File)
	if permalink && env.Links != nil {
		href = env.Links.PostURL(att)
	}
	return fmt.Sprintf("<a href='%s'>%s</a>", html.EscapeString(href), AttachmentImage(env, att, m, size, extra))
}

// GalleryShortcode is a port of gallery_shortcode() for themes without
// HTML5 gallery support (Twenty Twelve).
func GalleryShortcode(env *Env, sc *Shortcode) string {
	env.galleryInstance++
	instance := env.galleryInstance
	columns := atoiDefault(sc.Attr("columns", "3"), 3)
	size := sc.Attr("size", "thumbnail")
	link := sc.Attr("link", "")
	postID := int64(0)
	if env.Post != nil {
		postID = env.Post.ID
	}
	if v := sc.Attr("id", ""); v != "" {
		postID = int64(atoiDefault(v, 0))
	}

	var atts []db.Post
	var err error
	if ids := parseIDList(sc.Attr("ids", sc.Attr("include", ""))); len(ids) > 0 {
		var all []db.Post
		all, _, err = db.ListPosts(env.Ctx, env.Q, db.PostQuery{Type: db.TypeAttachment, Statuses: []string{db.StatusInherit}, IDs: ids})
		byID := map[int64]db.Post{}
		for _, p := range all {
			byID[p.ID] = p
		}
		for _, id := range ids { // post__in order
			if p, ok := byID[id]; ok {
				atts = append(atts, p)
			}
		}
	} else {
		atts, err = db.Children(env.Ctx, env.Q, postID, db.TypeAttachment)
	}
	if err != nil {
		return ""
	}
	images := atts[:0]
	for _, a := range atts {
		if strings.HasPrefix(a.MimeType, "image/") {
			images = append(images, a)
		}
	}
	if len(images) == 0 {
		return ""
	}
	ids := make([]int64, len(images))
	for i, a := range images {
		ids[i] = a.ID
	}
	media, err := db.MediaByIDs(env.Ctx, env.Q, ids)
	if err != nil {
		return ""
	}

	if env.IsFeed {
		var b strings.Builder
		b.WriteString("\n")
		for _, a := range images {
			if m := media[a.ID]; m != nil {
				b.WriteString(AttachmentLink(env, &a, m, size, link == "", nil))
				b.WriteString("\n")
			}
		}
		return b.String()
	}

	selector := fmt.Sprintf("gallery-%d", instance)
	itemWidth := 100
	if columns > 0 {
		itemWidth = 100 / columns
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\t\t<style>\n\t\t\t#%[1]s {\n\t\t\t\tmargin: auto;\n\t\t\t}\n\t\t\t#%[1]s .gallery-item {\n\t\t\t\tfloat: left;\n\t\t\t\tmargin-top: 10px;\n\t\t\t\ttext-align: center;\n\t\t\t\twidth: %[2]d%%;\n\t\t\t}\n\t\t\t#%[1]s img {\n\t\t\t\tborder: 2px solid #cfcfcf;\n\t\t\t}\n\t\t\t#%[1]s .gallery-caption {\n\t\t\t\tmargin-left: 0;\n\t\t\t}\n\t\t\t/* see gallery_shortcode() in wp-includes/media.php */\n\t\t</style>\n\t\t", selector, itemWidth)
	fmt.Fprintf(&b, "<div id='%s' class='gallery galleryid-%d gallery-columns-%d gallery-size-%s'>", selector, postID, columns, sanitizeHTMLClass(size))
	i := 0
	for _, a := range images {
		m := media[a.ID]
		if m == nil {
			continue
		}
		caption := strings.TrimSpace(a.Excerpt)
		var extra map[string]string
		if caption != "" {
			extra = map[string]string{"aria-describedby": fmt.Sprintf("%s-%d", selector, a.ID)}
		}
		var img string
		switch link {
		case "file":
			img = AttachmentLink(env, &a, m, size, false, extra)
		case "none":
			img = AttachmentImage(env, &a, m, size, extra)
		default:
			img = AttachmentLink(env, &a, m, size, true, extra)
		}
		orientation := "landscape"
		if m.Height > m.Width {
			orientation = "portrait"
		}
		b.WriteString("<dl class='gallery-item'>")
		fmt.Fprintf(&b, "\n\t\t\t<dt class='gallery-icon %s'>\n\t\t\t\t%s\n\t\t\t</dt>", orientation, img)
		if caption != "" {
			fmt.Fprintf(&b, "\n\t\t\t\t<dd class='wp-caption-text gallery-caption' id='%s-%d'>\n\t\t\t\t%s\n\t\t\t\t</dd>", selector, a.ID, Texturize(caption, nil))
		}
		b.WriteString("</dl>")
		i++
		if columns > 0 && i%columns == 0 {
			b.WriteString(`<br style="clear: both" />`)
		}
	}
	if columns > 0 && i%columns != 0 {
		b.WriteString("\n\t\t\t<br style='clear: both' />")
	}
	b.WriteString("\n\t\t</div>\n")
	return b.String()
}

// CaptionShortcode is a port of img_caption_shortcode() (non-HTML5 markup).
func CaptionShortcode(env *Env, sc *Shortcode) string {
	content := sc.Content
	caption, hasCaption := sc.Attrs["caption"]
	if !hasCaption {
		if img, rest, ok := splitCaptionImage(content); ok {
			content, caption = img, strings.TrimSpace(rest)
		}
	}
	width := atoiDefault(sc.Attr("width", ""), 0)
	if width < 1 || caption == "" {
		return content
	}
	id := sanitizeHTMLClass(sc.Attr("id", ""))
	captionID := sanitizeHTMLClass(sc.Attr("caption_id", ""))
	if captionID == "" && id != "" {
		captionID = "caption-" + strings.ReplaceAll(id, "_", "-")
	}
	var idAttr, captionIDAttr, describedBy string
	if id != "" {
		idAttr = fmt.Sprintf(`id="%s" `, html.EscapeString(id))
	}
	if captionID != "" {
		captionIDAttr = fmt.Sprintf(`id="%s" `, html.EscapeString(captionID))
		describedBy = fmt.Sprintf(`aria-describedby="%s" `, html.EscapeString(captionID))
	}
	class := strings.TrimSpace("wp-caption " + sc.Attr("align", "alignnone") + " " + sc.Attr("class", ""))
	style := fmt.Sprintf(`style="width: %dpx" `, width+10)
	return fmt.Sprintf(`<div %s%sclass="%s">%s<p %sclass="wp-caption-text">%s</p></div>`,
		idAttr, style, html.EscapeString(class), strings.Replace(content, "<img ", "<img "+describedBy, 1), captionIDAttr, caption)
}

// splitCaptionImage matches '#((?:<a [^>]+>\s*)?<img [^>]+>(?:\s*</a>)?)(.*)#is'.
func splitCaptionImage(s string) (string, string, bool) {
	i := strings.Index(s, "<img ")
	if i < 0 {
		return "", "", false
	}
	start := i
	if a := strings.LastIndex(s[:i], "<a "); a >= 0 && strings.TrimSpace(s[strings.IndexByte(s[a:], '>')+a+1:i]) == "" {
		start = a
	}
	end := strings.IndexByte(s[i:], '>')
	if end < 0 {
		return "", "", false
	}
	end += i + 1
	if rest := strings.TrimLeft(s[end:], " \t\r\n"); strings.HasPrefix(rest, "</a>") {
		end = len(s) - len(rest) + 4
	}
	return s[start:end], s[end:], true
}

func sanitizeHTMLClass(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isWordChar(c) || c == '-' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func parseIDList(s string) []int64 {
	var ids []int64
	for _, f := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
