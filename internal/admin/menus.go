package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// Menus (nav-menus.php): build navigation menus from pages, posts,
// categories, tags and custom links, nest them and assign them to the
// theme's location.

func (a *Admin) registerMenus(mux *http.ServeMux) {
	mux.Handle("GET /admin/menus", a.page(auth.CapEditThemeOptions, a.menusPage))
	mux.Handle("POST /admin/menus/new", a.page(auth.CapEditThemeOptions, a.menuCreate))
	mux.Handle("POST /admin/menus/{id}", a.page(auth.CapEditThemeOptions, a.menuSave))
	mux.Handle("POST /admin/menus/{id}/delete", a.page(auth.CapEditThemeOptions, a.menuDelete))
	mux.Handle("GET /admin/api/menu-search", a.page(auth.CapEditThemeOptions, a.menuSearch))
}

// menuLocations are the theme's locations (register_nav_menu).
var menuLocations = []struct{ ID, Label string }{{"primary", "Primary Menu"}}

type menuItemRow struct {
	db.MenuItem
	Depth     int
	Label     string // shown in the bar
	TypeLabel string
	Original  string // the object's title (placeholder of the label field)
	Link      string
}

type menuChoice struct {
	ID, Title string
	Level     int
}

type menusData struct {
	basePage
	Menus     []db.Menu
	Editing   *db.Menu // the menu being edited (Menu is the admin menu)
	Items     []menuItemRow
	Locations []option
	Pages     []menuChoice
	Posts     []menuChoice
	Cats      []menuChoice
	Tags      []menuChoice
	Error     string
	Blank     menuItemRow // template for new items
}

var menuKinds = map[string]string{db.TypePage: "Page", db.TypePost: "Post", db.TaxCategory: "Category", db.TaxTag: "Tag", "custom": "Custom Link"}

// GET /admin/menus?menu=ID
func (a *Admin) menusPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderMenus(w, r, c, atoi64(r.URL.Query().Get("menu")), "")
}

func (a *Admin) renderMenus(w http.ResponseWriter, r *http.Request, c *reqCtx, menuID int64, errMsg string) error {
	ctx := r.Context()
	d := &menusData{basePage: a.base(ctx, c, "Menus", "appearance-menus"), Error: errMsg}
	d.Scripts = []string{"menus.js"}
	if msg, ok := map[string]notice{"saved": {"success", "Menu saved."}, "created": {"success", "Menu created. Add items from the left."},
		"deleted": {"success", "Menu deleted."}}[r.URL.Query().Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	var err error
	if d.Menus, err = db.Menus(ctx, a.db.R); err != nil {
		return err
	}
	if menuID == 0 && len(d.Menus) > 0 {
		menuID = c.settings.MenuLocations["primary"]
		if menuID == 0 {
			menuID = d.Menus[0].ID
		}
	}
	if menuID != 0 {
		if d.Editing, err = db.GetMenu(ctx, a.db.R, menuID); errors.Is(err, sql.ErrNoRows) {
			d.Editing = nil
		} else if err != nil {
			return err
		}
	}
	if d.Editing != nil {
		items, err := db.MenuItems(ctx, a.db.R, d.Editing.ID)
		if err != nil {
			return err
		}
		d.Items = a.menuRows(ctx, c, items)
		for _, l := range menuLocations {
			d.Locations = append(d.Locations, option{Value: l.ID, Label: l.Label, Selected: c.settings.MenuLocations[l.ID] == d.Editing.ID})
		}
	}
	// Choices for the left boxes.
	pages, err := db.AllPages(ctx, a.db.R)
	if err != nil {
		return err
	}
	for _, p := range pageTreeOrder(pages) {
		if p.post.Status == db.StatusPublish {
			d.Pages = append(d.Pages, menuChoice{strconv.FormatInt(p.post.ID, 10), p.post.Title, p.level})
		}
	}
	posts, _, err := db.AdminPosts(ctx, a.db.R, db.AdminPostQuery{Type: db.TypePost, Status: db.StatusPublish, Limit: 30})
	if err != nil {
		return err
	}
	for _, p := range posts {
		d.Posts = append(d.Posts, menuChoice{ID: strconv.FormatInt(p.ID, 10), Title: p.Title})
	}
	for tax, dst := range map[string]*[]menuChoice{db.TaxCategory: &d.Cats, db.TaxTag: &d.Tags} {
		terms, err := db.Terms(ctx, a.db.R, tax)
		if err != nil {
			return err
		}
		tree, lv := termTree(terms)
		for _, t := range tree {
			*dst = append(*dst, menuChoice{strconv.FormatInt(t.ID, 10), t.Name, lv[t.ID]})
		}
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "menus", d)
}

type pageNode struct {
	post  db.Post
	level int
}

// pageTreeOrder orders pages parent-first like the page list.
func pageTreeOrder(pages []db.Post) []pageNode {
	kids := map[int64][]db.Post{}
	ids := map[int64]bool{}
	for _, p := range pages {
		ids[p.ID] = true
	}
	for _, p := range pages {
		parent := p.ParentID
		if !ids[parent] {
			parent = 0
		}
		kids[parent] = append(kids[parent], p)
	}
	var out []pageNode
	seen := map[int64]bool{}
	var walk func(parent int64, level int)
	walk = func(parent int64, level int) {
		for _, p := range kids[parent] {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out = append(out, pageNode{p, level})
			walk(p.ID, level+1)
		}
	}
	walk(0, 0)
	return out
}

// menuRows adds depth and display data to stored items.
func (a *Admin) menuRows(ctx context.Context, c *reqCtx, items []db.MenuItem) []menuItemRow {
	depth := map[int64]int{}
	var out []menuItemRow
	for _, it := range items {
		row := menuItemRow{MenuItem: it, TypeLabel: menuKinds[it.Kind]}
		if it.ParentID != 0 {
			row.Depth = depth[it.ParentID] + 1
		}
		depth[it.ID] = row.Depth
		switch it.Kind {
		case db.TypePage, db.TypePost:
			if p, err := db.GetPost(ctx, a.db.R, it.ObjectID); err == nil {
				row.Original, row.Link = p.Title, c.links.PostURL(p)
				if p.Status != db.StatusPublish {
					row.TypeLabel += " (not published)"
				}
			} else {
				row.TypeLabel += " (missing)"
			}
		case db.TaxCategory, db.TaxTag:
			if t, err := db.GetTerm(ctx, a.db.R, it.ObjectID); err == nil && t != nil {
				row.Original, row.Link = t.Name, c.links.TermURL(t)
			}
		default:
			row.Link = it.URL
		}
		row.Label = it.Title
		if row.Label == "" {
			row.Label = row.Original
		}
		out = append(out, row)
	}
	return out
}

// POST /admin/menus/new
func (a *Admin) menuCreate(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	name := strings.TrimSpace(render.SanitizeText(r.PostFormValue("menu-name")))
	if name == "" || len(name) > 100 {
		return a.renderMenus(w, r, c, 0, "Please enter a menu name (up to 100 characters).")
	}
	m := &db.Menu{Name: name}
	if err := db.InsertMenu(ctx, a.db.W, m); err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "menu_created", "menu", m.ID, map[string]any{"name": name})
	return seeOther(w, r, "/admin/menus?menu="+strconv.FormatInt(m.ID, 10)+"&msg=created")
}

// menuItemJSON is an item as submitted by menus.js.
type menuItemJSON struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	ObjectID  int64  `json:"object_id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	AttrTitle string `json:"attr_title"`
	Target    string `json:"target"`
	Classes   string `json:"classes"`
	Depth     int    `json:"depth"`
}

var reClasses = regexp.MustCompile(`^[A-Za-z0-9_ -]{0,200}$`)

// safeMenuURL accepts http(s), site-relative and fragment links, mailto:
// and tel: (no javascript: or data:).
func safeMenuURL(s string) bool {
	if s == "" || len(s) > 2000 || strings.ContainsAny(s, "\x00\r\n\t<>\"") {
		return false
	}
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") || strings.HasPrefix(s, "#") {
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto", "tel":
		return true
	}
	return false
}

// POST /admin/menus/{id}: name, locations and the item structure.
func (a *Admin) menuSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	menu, err := db.GetMenu(ctx, a.db.R, atoi64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	name := strings.TrimSpace(render.SanitizeText(r.PostFormValue("menu-name")))
	if name == "" || len(name) > 100 {
		return a.renderMenus(w, r, c, menu.ID, "Please enter a menu name (up to 100 characters).")
	}
	var in []menuItemJSON
	if err := json.Unmarshal([]byte(r.PostFormValue("items")), &in); err != nil || len(in) > 500 {
		return a.renderMenus(w, r, c, menu.ID, "The menu structure could not be read. Please reload the page.")
	}
	items := make([]db.MenuItem, 0, len(in))
	parents := make([]int, 0, len(in))
	var stack []int // index of the last item at each depth
	for i, x := range in {
		it := db.MenuItem{ID: x.ID, Kind: x.Kind, ObjectID: x.ObjectID,
			Title: strings.TrimSpace(render.SanitizeText(x.Title)), AttrTitle: strings.TrimSpace(render.SanitizeText(x.AttrTitle)),
			Classes: strings.Join(strings.Fields(x.Classes), " ")}
		if len(it.Title) > 500 || len(it.AttrTitle) > 500 || !reClasses.MatchString(it.Classes) {
			return a.renderMenus(w, r, c, menu.ID, "A menu item has an invalid label, title attribute or CSS class.")
		}
		if x.Target == "_blank" {
			it.Target = "_blank"
		}
		switch x.Kind {
		case db.TypePage, db.TypePost:
			p, err := db.GetPost(ctx, a.db.R, x.ObjectID)
			if err != nil || p.Type != x.Kind {
				return a.renderMenus(w, r, c, menu.ID, "A menu item points to a page or post that no longer exists.")
			}
		case db.TaxCategory, db.TaxTag:
			t, err := db.GetTerm(ctx, a.db.R, x.ObjectID)
			if err != nil || t == nil || t.Taxonomy != x.Kind {
				return a.renderMenus(w, r, c, menu.ID, "A menu item points to a category or tag that no longer exists.")
			}
		case "custom":
			u := strings.TrimSpace(x.URL)
			if !safeMenuURL(u) {
				return a.renderMenus(w, r, c, menu.ID, "Custom links need an http(s), relative, mailto: or tel: URL.")
			}
			if it.Title == "" {
				return a.renderMenus(w, r, c, menu.ID, "Custom links need a navigation label.")
			}
			it.URL, it.ObjectID = u, 0
		default:
			return a.renderMenus(w, r, c, menu.ID, "Unknown menu item type.")
		}
		// Depth: at most one level deeper than the item before, max 10.
		depth := x.Depth
		if depth < 0 || i == 0 {
			depth = 0
		}
		if depth > len(stack) {
			depth = len(stack)
		}
		if depth > 10 {
			depth = 10
		}
		stack = append(stack[:depth], i)
		parent := -1
		if depth > 0 {
			parent = stack[depth-1]
		}
		items = append(items, it)
		parents = append(parents, parent)
	}
	locs := map[string]int64{}
	db.GetOption(ctx, a.db.R, "nav_menu_locations", &locs)
	if locs == nil {
		locs = map[string]int64{}
	}
	for _, l := range menuLocations {
		if r.PostFormValue("menu-locations["+l.ID+"]") != "" {
			locs[l.ID] = menu.ID
		} else if locs[l.ID] == menu.ID {
			delete(locs, l.ID)
		}
	}
	err = a.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.RenameMenu(ctx, tx, menu.ID, name); err != nil {
			return err
		}
		if err := db.SaveMenuItems(ctx, tx, menu.ID, items, parents); err != nil {
			return err
		}
		return db.SetOption(ctx, tx, "nav_menu_locations", locs)
	})
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "menu_saved", "menu", menu.ID, map[string]any{"items": len(items)})
	a.site.Invalidate()
	return seeOther(w, r, "/admin/menus?menu="+strconv.FormatInt(menu.ID, 10)+"&msg=saved")
}

// POST /admin/menus/{id}/delete
func (a *Admin) menuDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	id := atoi64(r.PathValue("id"))
	locs := map[string]int64{}
	db.GetOption(ctx, a.db.R, "nav_menu_locations", &locs)
	for k, v := range locs {
		if v == id {
			delete(locs, k)
		}
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.DeleteMenu(ctx, tx, id); err != nil {
			return err
		}
		return db.SetOption(ctx, tx, "nav_menu_locations", locs)
	})
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "menu_deleted", "menu", id, nil)
	a.site.Invalidate()
	return seeOther(w, r, "/admin/menus?msg=deleted")
}

// GET /admin/api/menu-search?kind=post|page&s=…
func (a *Admin) menuSearch(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	kind := r.URL.Query().Get("kind")
	if kind != db.TypePost && kind != db.TypePage {
		return badRequest("Invalid kind.")
	}
	posts, _, err := db.AdminPosts(r.Context(), a.db.R, db.AdminPostQuery{Type: kind, Status: db.StatusPublish,
		Search: r.URL.Query().Get("s"), Limit: 30})
	if err != nil {
		return err
	}
	out := []map[string]string{}
	for _, p := range posts {
		out = append(out, map[string]string{"id": strconv.FormatInt(p.ID, 10), "title": p.Title})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
