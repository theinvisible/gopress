package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func TestTerms(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if rec := e.do("GET", "/admin/categories/", e.session(auth.RoleAuthor, db.SessionFull), nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("author: %d", rec.Code)
	}
	ed := e.session(auth.RoleEditor, db.SessionFull)
	for _, p := range []string{"/admin/categories/", "/admin/tags/"} {
		if rec := e.do("GET", p, ed, nil, nil); rec.Code != 200 {
			t.Errorf("editor GET %s: %d", p, rec.Code)
		}
	}
	add := func(base string, f url.Values) *http.Response { return post(e, base, ed, f) }
	if res := add("/admin/categories/", url.Values{"tag-name": {"Linux"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add: %d", res.StatusCode)
	}
	linux, _ := db.TermBySlug(ctx, e.d.R, db.TaxCategory, "linux")
	if linux == nil {
		t.Fatal("category not created")
	}
	if res := add("/admin/categories/", url.Values{"tag-name": {"Debian"}, "parent": {strconv.FormatInt(linux.ID, 10)}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add child: %d", res.StatusCode)
	}
	debian, _ := db.TermBySlug(ctx, e.d.R, db.TaxCategory, "debian")
	// Same name below the same parent; explicit slug taken; cycles.
	for _, f := range []url.Values{
		{"tag-name": {"linux"}},
		{"tag-name": {"Other"}, "slug": {"debian"}},
	} {
		if res := add("/admin/categories/", f); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: %d", f, res.StatusCode)
		}
	}
	lid := strconv.FormatInt(linux.ID, 10)
	if res := post(e, "/admin/categories/"+lid+"/edit", ed, url.Values{"tag-name": {"Linux"}, "parent": {strconv.FormatInt(debian.ID, 10)}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("cycle accepted: %d", res.StatusCode)
	}
	// Assign the post, rename the slug: the old archive URL redirects.
	db.AddPostTerm(ctx, e.d.W, 10, linux.ID, 0)
	e.pub.Invalidate()
	if rec := e.do("GET", "/category/linux/", nil, nil, nil); rec.Code != 200 {
		t.Fatalf("archive: %d", rec.Code)
	}
	rec := e.do("POST", "/admin/categories/"+lid+"/edit", ed, url.Values{"tag-name": {"GNU/Linux"}, "slug": {"gnu-linux"}, "quick": {"1"}, "_csrf": {ed.CSRF}},
		map[string]string{"Origin": home, "Accept": "application/json"})
	var got map[string]string
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["slug"] != "gnu-linux" || got["name"] != "GNU/Linux" {
		t.Fatalf("quick edit: %d %v", rec.Code, got)
	}
	if rec := e.do("GET", "/category/linux/", nil, nil, nil); rec.Code != http.StatusMovedPermanently || !strings.HasSuffix(rec.Header().Get("Location"), "/category/gnu-linux/") {
		t.Errorf("old category URL: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// The default category cannot be deleted; deleting the only category
	// of a post gives it the default one.
	if res := post(e, "/admin/categories/1/delete", ed, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("default category deleted: %d", res.StatusCode)
	}
	if res := post(e, "/admin/categories/bulk", ed, url.Values{"action": {"delete"}, "delete_tags[]": {lid}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	terms, _ := db.PostTerms(ctx, e.d.R, []int64{10})
	if len(terms[10]) != 1 || terms[10][0].ID != 1 {
		t.Errorf("post terms after delete: %+v", terms[10])
	}
	if d, _ := db.GetTerm(ctx, e.d.R, debian.ID); d == nil || d.ParentID != 0 {
		t.Errorf("child not moved up: %+v", d)
	}
	// Tags; the post list filters by tag.
	if res := add("/admin/tags/", url.Values{"tag-name": {"Go <b>lang</b>"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add tag: %d", res.StatusCode)
	}
	tags, _ := db.Terms(ctx, e.d.R, db.TaxTag)
	if len(tags) != 1 || strings.Contains(tags[0].Name, "<") || tags[0].Slug != "go-lang" {
		t.Errorf("tag = %+v", tags)
	}
	if rec := e.do("GET", "/admin/posts/?tag="+strconv.FormatInt(tags[0].ID, 10), ed, nil, nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "Hello") {
		t.Errorf("tag filter: %d", rec.Code)
	}
	// Only administrators change the default category.
	if res := post(e, "/admin/categories/"+strconv.FormatInt(debian.ID, 10)+"/default", ed, nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("editor changes default: %d", res.StatusCode)
	}
}
