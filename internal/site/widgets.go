package site

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/theme"
)

// WidgetRenderer renders a widget type provided by a plugin. It returns
// the inner HTML (without the <aside> wrapper) or "" to skip the widget.
type WidgetRenderer func(rq WidgetContext, settings map[string]any) string

// WidgetContext gives plugin widgets access to request data.
type WidgetContext interface {
	Home() string
}

func (rq *request) Home() string { return rq.st.links.Home() }

// sidebar renders the widgets of a sidebar (dynamic_sidebar()) with
// Twenty Twelve's before/after markup.
func (rq *request) sidebar(id string) string {
	widgets, err := db.Widgets(rq.ctx, rq.site.db.R, id)
	if err != nil {
		rq.site.log.Error("load widgets", "sidebar", id, "err", err)
		return ""
	}
	var b strings.Builder
	for _, w := range widgets {
		inner, class := rq.widget(&w)
		if inner == "" {
			continue
		}
		fmt.Fprintf(&b, `<aside id="%s" class="widget %s">%s</aside>`, esc(w.ID), class, inner)
	}
	return b.String()
}

func widgetTitle(settings map[string]any, def string) string {
	title := def
	if t, ok := settings["title"].(string); ok && t != "" {
		title = t
	}
	if title == "" {
		return ""
	}
	return `<h3 class="widget-title">` + escKeepEntities(render.Texturize(title, nil)) + `</h3>`
}

func settingBool(settings map[string]any, key string) bool {
	switch v := settings[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != "" && v != "0"
	}
	return false
}

func settingInt(settings map[string]any, key string, def int) int {
	switch v := settings[key].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// widget renders one widget, returning its inner HTML and CSS class.
func (rq *request) widget(w *db.Widget) (string, string) {
	st := rq.st
	l := st.links
	set := w.Settings
	switch w.Type {
	case "search":
		return widgetTitle(set, "") + rq.searchForm(), "widget_search"

	case "block":
		content, _ := set["content"].(string)
		if content == "" {
			return "", ""
		}
		return "\n" + content + "\n", "widget_block"

	case "text":
		text, _ := set["text"].(string)
		if settingBool(set, "filter") || set["filter"] == "content" {
			text = render.Autop(text)
		}
		return widgetTitle(set, "") + "\t\t\t<div class=\"textwidget\">" + text + "</div>\n\t\t", "widget_text"

	case "recent-posts":
		n := settingInt(set, "number", 5)
		if n < 1 || n > 30 {
			n = 5
		}
		posts, _, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{Type: db.TypePost, Limit: n})
		if err != nil || len(posts) == 0 {
			return "", ""
		}
		var b strings.Builder
		fmt.Fprintf(&b, "\n\t\t%s\n\t\t<ul>\n", widgetTitle(set, "Recent Posts"))
		for i := range posts {
			p := &posts[i]
			fmt.Fprintf(&b, "\t\t\t\t\t\t\t\t\t\t\t<li>\n\t\t\t\t\t<a href=\"%s\">%s</a>\n", esc(l.PostURL(p)), rq.title(p.Title))
			if settingBool(set, "show_date") {
				fmt.Fprintf(&b, "\t\t\t\t\t\t\t\t\t\t\t<span class=\"post-date\">%s</span>\n", esc(rq.formatDate(p)))
			}
			b.WriteString("\t\t\t\t\t\t\t\t\t</li>\n")
		}
		b.WriteString("\t\t\t\t\t</ul>\n\n\t\t")
		return b.String(), "widget_recent_entries"

	case "categories":
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\t\t\t<ul>\n", widgetTitle(set, "Categories"))
		count := settingBool(set, "count")
		if settingBool(set, "hierarchical") {
			rq.categoryTree(&b, 0, 1, count)
		} else {
			for _, t := range st.ix.cats {
				if t.Count > 0 {
					rq.categoryItem(&b, t, 1, count)
					b.WriteString("</li>\n")
				}
			}
		}
		b.WriteString("\t\t\t</ul>\n\n\t\t\t")
		return b.String(), "widget_categories"

	case "tag_cloud":
		cloud := rq.tagCloud()
		if cloud == "" {
			return "", ""
		}
		return widgetTitle(set, "Tags") + `<div class="tagcloud">` + cloud + "</div>\n", "widget_tag_cloud"

	case "archives":
		months, err := db.MonthlyArchives(rq.ctx, rq.site.db.R)
		if err != nil || len(months) == 0 {
			return "", ""
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\t\t\t<ul>\n", widgetTitle(set, "Archives"))
		for i, m := range months {
			indent := "\t"
			if i == 0 {
				indent = "\t\t\t\t"
			}
			after := ""
			if settingBool(set, "count") {
				after = fmt.Sprintf("&nbsp;(%d)", m.Count)
			}
			fmt.Fprintf(&b, "%s<li><a href='%s'>%s %d</a>%s</li>\n", indent, esc(l.DateURL(m.Year, m.Month, 0)), monthName(m.Month), m.Year, after)
		}
		b.WriteString("\t\t\t</ul>\n\n\t\t\t")
		return b.String(), "widget_archive"

	case "meta":
		return fmt.Sprintf("%s\n\t\t<ul>\n\t\t\t\t\t\t<li><a href=\"%s\">Log in</a></li>\n\t\t\t<li><a href=\"%s\">Entries feed</a></li>\n\t\t\t<li><a href=\"%s\">Comments feed</a></li>\n\n\t\t\t<li><a href=\"https://github.com/theinvisible/gopress\">GoPress</a></li>\n\t\t</ul>\n\n\t\t",
			widgetTitle(set, "Meta"), esc(l.URL("/wp-login.php")), esc(l.URL("/feed/")), esc(l.URL("/comments/feed/"))), "widget_meta"
	}
	if r := rq.site.widgets[w.Type]; r != nil {
		if inner := r(rq, set); inner != "" {
			return inner, "widget_" + sanitizeClass(w.Type)
		}
	}
	return "", ""
}

// RegisterWidget adds a plugin widget type.
func (s *Site) RegisterWidget(typ string, r WidgetRenderer) {
	if s.widgets == nil {
		s.widgets = map[string]WidgetRenderer{}
	}
	s.widgets[typ] = r
}

func (rq *request) formatDate(p *db.Post) string {
	return formatPHPDateIn(rq.st.s.DateFormat, p.DateGMT.Time, rq.st.s.Loc)
}

func (rq *request) categoryItem(b *strings.Builder, t *db.Term, depth int, count bool) {
	indent := strings.Repeat("\t", depth)
	after := ""
	if count {
		after = fmt.Sprintf(" (%d)", t.Count)
	}
	fmt.Fprintf(b, "%s<li class=\"cat-item cat-item-%d\"><a href=\"%s\">%s</a>%s\n", indent, t.ID, esc(rq.st.links.TermURL(t)), escKeepEntities(render.Texturize(t.Name, nil)), after)
}

func (rq *request) categoryTree(b *strings.Builder, parent int64, depth int, count bool) {
	for _, t := range rq.st.ix.cats {
		if t.ParentID != parent || t.Count == 0 && !rq.hasNonEmptyChild(t) {
			continue
		}
		rq.categoryItem(b, t, depth, count)
		var sub strings.Builder
		rq.categoryTree(&sub, t.ID, depth+1, count)
		if sub.Len() > 0 {
			fmt.Fprintf(b, "%s<ul class='children'>\n%s%s</ul>\n", strings.Repeat("\t", depth), sub.String(), strings.Repeat("\t", depth))
		}
		b.WriteString(strings.Repeat("\t", depth) + "</li>\n")
	}
}

func (rq *request) hasNonEmptyChild(t *db.Term) bool {
	for _, id := range rq.st.ix.termAndChildren(t)[1:] {
		if c := rq.st.ix.terms[id]; c != nil && c.Count > 0 {
			return true
		}
	}
	return false
}

// tagCloud is wp_tag_cloud() with the default settings (8–22pt,
// logarithmic scale, top 45 tags by count, sorted by name).
func (rq *request) tagCloud() string {
	var tags []*db.Term
	for _, t := range rq.st.ix.tags {
		if t.Count > 0 {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return ""
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].Count > tags[j].Count })
	if len(tags) > 45 {
		tags = tags[:45]
	}
	sort.SliceStable(tags, func(i, j int) bool { return strings.ToLower(tags[i].Name) < strings.ToLower(tags[j].Name) })
	scale := func(n int) float64 { return math.Round(math.Log10(float64(n)+1) * 100) }
	minC, maxC := math.Inf(1), math.Inf(-1)
	for _, t := range tags {
		c := scale(t.Count)
		minC, maxC = math.Min(minC, c), math.Max(maxC, c)
	}
	spread := maxC - minC
	if spread <= 0 {
		spread = 1
	}
	step := (22.0 - 8.0) / spread
	var b strings.Builder
	b.WriteString("<ul class='wp-tag-cloud' role='list'>\n")
	for i, t := range tags {
		size := 8 + (scale(t.Count)-minC)*step
		items := "items"
		if t.Count == 1 {
			items = "item"
		}
		name := escKeepEntities(render.Texturize(t.Name, nil))
		fmt.Fprintf(&b, "\t<li><a href=\"%s\" class=\"tag-cloud-link tag-link-%d tag-link-position-%d\" style=\"font-size: %spt;\" aria-label=\"%s (%d %s)\">%s</a></li>\n",
			esc(rq.st.links.TermURL(t)), t.ID, i+1, strconv.FormatFloat(size, 'g', 14, 64), esc(t.Name), t.Count, items, name)
	}
	b.WriteString("</ul>\n")
	return b.String()
}

func formatPHPDateIn(format string, t time.Time, loc *time.Location) string {
	return theme.FormatPHPDate(format, t.In(loc))
}
