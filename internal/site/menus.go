package site

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// navMenu is wp_nav_menu() for a menu: the menu in its container, or ""
// if the menu does not exist or has no visible items. ulClass is the
// menu_class ("nav-menu" in the header, "menu" in widgets).
func (rq *request) navMenu(menuID int64, ulClass string, current *db.Post, isFront bool) string {
	menu, err := db.GetMenu(rq.ctx, rq.site.db.R, menuID)
	if err != nil {
		return ""
	}
	items, err := db.MenuItems(rq.ctx, rq.site.db.R, menuID)
	if err != nil || len(items) == 0 {
		return ""
	}
	ix := rq.st.ix
	l := rq.st.links
	type node struct {
		item     db.MenuItem
		url      string
		title    string
		children []int
		current  bool
		page     bool
	}
	nodes := make([]node, 0, len(items))
	index := map[int64]int{}
	home := strings.TrimRight(l.Home(), "/")
	for _, it := range items {
		n := node{item: it, title: esc(it.Title), url: it.URL}
		switch it.Kind {
		case db.TypePage, db.TypePost:
			p := ix.posts[it.ObjectID]
			if p == nil || !ix.isPublic(p) {
				continue // drafts, trash: hidden
			}
			n.url = l.PostURL(p)
			if it.Title == "" {
				n.title = rq.title(p.Title)
			}
			n.page = p.Type == db.TypePage
			n.current = current != nil && current.ID == p.ID
		case db.TaxCategory, db.TaxTag:
			t := ix.terms[it.ObjectID]
			if t == nil {
				continue
			}
			n.url = l.TermURL(t)
			if it.Title == "" {
				n.title = esc(t.Name)
			}
		default:
			n.current = isFront && strings.TrimRight(it.URL, "/") == home
		}
		index[it.ID] = len(nodes)
		nodes = append(nodes, n)
	}
	var roots []int
	for i := range nodes {
		if p, ok := index[nodes[i].item.ParentID]; ok && nodes[i].item.ParentID != 0 {
			nodes[p].children = append(nodes[p].children, i)
		} else {
			roots = append(roots, i)
		}
	}
	if len(roots) == 0 {
		return ""
	}
	// Ancestors of the current item.
	parentOf := map[int]int{}
	for i := range nodes {
		for _, c := range nodes[i].children {
			parentOf[c] = i
		}
	}
	ancestor, parent := map[int]bool{}, map[int]bool{}
	for i := range nodes {
		if !nodes[i].current {
			continue
		}
		if p, ok := parentOf[i]; ok {
			parent[p] = true
		}
		for p, ok := parentOf[i]; ok && !ancestor[p]; p, ok = parentOf[p] {
			ancestor[p] = true
		}
	}
	slug := sanitizeClass(strings.ToLower(menu.Name))
	var b strings.Builder
	fmt.Fprintf(&b, "<div class=\"menu-%s-container\"><ul id=\"menu-%s\" class=\"%s\">", slug, slug, ulClass)
	var walk func(list []int, depth int)
	walk = func(list []int, depth int) {
		for _, i := range list {
			n := &nodes[i]
			it := n.item
			var cls []string
			if it.Classes != "" {
				cls = append(cls, strings.Fields(it.Classes)...)
			}
			typ, obj := "custom", "custom"
			switch it.Kind {
			case db.TypePage, db.TypePost:
				typ, obj = "post_type", it.Kind
			case db.TaxCategory, db.TaxTag:
				typ, obj = "taxonomy", it.Kind
			}
			cls = append(cls, "menu-item", "menu-item-type-"+typ, "menu-item-object-"+obj)
			if it.Kind == "custom" && strings.TrimRight(it.URL, "/") == home {
				cls = append(cls, "menu-item-home")
			}
			if n.current {
				cls = append(cls, "current-menu-item")
				if n.page {
					cls = append(cls, "page_item", "page-item-"+strconv.FormatInt(it.ObjectID, 10), "current_page_item")
				}
			}
			if ancestor[i] {
				cls = append(cls, "current-menu-ancestor")
				if parent[i] {
					cls = append(cls, "current-menu-parent")
				}
				if n.page {
					cls = append(cls, "current_page_ancestor")
					if parent[i] {
						cls = append(cls, "current_page_parent")
					}
				}
			}
			if len(n.children) > 0 {
				cls = append(cls, "menu-item-has-children")
			}
			cls = append(cls, "menu-item-"+strconv.FormatInt(it.ID, 10))
			fmt.Fprintf(&b, "<li id=\"menu-item-%d\" class=\"%s\"><a", it.ID, esc(strings.Join(cls, " ")))
			if it.AttrTitle != "" {
				fmt.Fprintf(&b, " title=\"%s\"", esc(it.AttrTitle))
			}
			if it.Target == "_blank" {
				b.WriteString(` target="_blank" rel="noopener"`)
			}
			fmt.Fprintf(&b, " href=\"%s\"", esc(n.url))
			if n.current {
				b.WriteString(` aria-current="page"`)
			}
			fmt.Fprintf(&b, ">%s</a>", n.title)
			if len(n.children) > 0 {
				b.WriteString("\n" + strings.Repeat("\t", depth+1) + "<ul class=\"sub-menu\">\n")
				walk(n.children, depth+1)
				b.WriteString(strings.Repeat("\t", depth+1) + "</ul>\n")
			}
			b.WriteString("</li>\n")
		}
	}
	walk(roots, 0)
	b.WriteString("</ul></div>")
	return b.String()
}

// primaryMenu is the header navigation: the menu at the "primary"
// location or the page list (wp_page_menu) as WordPress' fallback.
func (rq *request) primaryMenu(isFront bool, current *db.Post) string {
	if id := rq.st.s.MenuLocations["primary"]; id != 0 {
		if m := rq.navMenu(id, "nav-menu", current, isFront); m != "" {
			return m
		}
	}
	return rq.pageMenu(isFront, current)
}

// curPostObj returns the post of a singular request (or nil).
func (rq *request) curPostObj() *db.Post {
	if rq.curPost == 0 {
		return nil
	}
	return rq.st.ix.posts[rq.curPost]
}
