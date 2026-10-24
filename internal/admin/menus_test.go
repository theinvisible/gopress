package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func TestMenus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	if rec := e.do("GET", "/admin/menus", e.session(auth.RoleEditor, db.SessionFull), nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("editor: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/menus", admin, nil, nil); rec.Code != 200 {
		t.Fatalf("empty page: %d", rec.Code)
	}
	res := post(e, "/admin/menus/new", admin, url.Values{"menu-name": {"Main"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", res.StatusCode)
	}
	menus, _ := db.Menus(ctx, e.d.R)
	id := strconv.FormatInt(menus[0].ID, 10)
	if rec := e.do("GET", "/admin/menus?menu="+id, admin, nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "gp-menu-item-tpl") {
		t.Fatalf("edit page: %d", rec.Code)
	}
	items := `[{"id":0,"kind":"custom","url":"https://example.org/","title":"Home","depth":0},
		{"id":0,"kind":"post","object_id":10,"title":"","depth":1,"classes":"special one","target":"_blank"},
		{"id":0,"kind":"category","object_id":1,"title":"All <b>news</b>","depth":5}]`
	for _, bad := range []string{
		`[{"kind":"custom","url":"javascript:alert(1)","title":"x"}]`,
		`[{"kind":"custom","url":"//evil.example/","title":"x"}]`,
		`[{"kind":"post","object_id":999}]`,
		`[{"kind":"post","object_id":1}]`, // a term id as post
		`[{"kind":"page","object_id":10}]`, // a post as page
		`[{"kind":"custom","url":"/x","title":"x","classes":"a\"onclick=\"x"}]`,
		`not json`,
	} {
		if res := post(e, "/admin/menus/"+id, admin, url.Values{"menu-name": {"Main"}, "items": {bad}}); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", bad, res.StatusCode)
		}
	}
	if res := post(e, "/admin/menus/"+id, admin, url.Values{"menu-name": {"Main"}, "items": {items}, "menu-locations[primary]": {"1"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	stored, _ := db.MenuItems(ctx, e.d.R, menus[0].ID)
	if len(stored) != 3 || stored[1].ParentID != stored[0].ID || stored[2].ParentID != stored[1].ID || stored[2].Title != "All news" {
		t.Fatalf("items = %+v", stored)
	}
	body := e.do("GET", "/hello/", nil, nil, nil).Body.String()
	for _, want := range []string{
		`<div class="menu-main-container"><ul id="menu-main" class="nav-menu">`,
		`class="menu-item menu-item-type-custom menu-item-object-custom menu-item-home current-menu-ancestor current-menu-parent menu-item-has-children menu-item-` + strconv.FormatInt(stored[0].ID, 10) + `"`,
		`class="special one menu-item menu-item-type-post_type menu-item-object-post current-menu-item menu-item-has-children menu-item-`,
		`target="_blank" rel="noopener" href="https://example.org/hello/" aria-current="page">Hello</a>`,
		`<ul class="sub-menu">`,
		`href="https://example.org/category/uncategorized/">All news</a>`,
	} {
		if !strings.Contains(body, want) {
			i := strings.Index(body, "site-navigation")
			t.Errorf("missing %q in %s", want, body[i:min(len(body), i+1500)])
		}
	}
	// Keeping ids on save; removing an item.
	keep := `[{"id":` + strconv.FormatInt(stored[0].ID, 10) + `,"kind":"custom","url":"https://example.org/","title":"Start","depth":0}]`
	post(e, "/admin/menus/"+id, admin, url.Values{"menu-name": {"Main"}, "items": {keep}, "menu-locations[primary]": {"1"}})
	if s, _ := db.MenuItems(ctx, e.d.R, menus[0].ID); len(s) != 1 || s[0].ID != stored[0].ID || s[0].Title != "Start" {
		t.Errorf("after update: %+v", s)
	}
	// Deleting the menu brings back the page list.
	post(e, "/admin/menus/"+id+"/delete", admin, nil)
	if body := e.do("GET", "/hello/", nil, nil, nil).Body.String(); !strings.Contains(body, `<div class="nav-menu"><ul>`) {
		t.Error("page list fallback missing")
	}
}
