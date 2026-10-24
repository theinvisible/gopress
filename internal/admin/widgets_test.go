package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func TestWidgets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if rec := e.do("GET", "/admin/widgets", e.session(auth.RoleEditor, db.SessionFull), nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("editor: %d", rec.Code)
	}
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	if !strings.Contains(e.do("GET", "/hello/", nil, nil, nil).Body.String(), "full-width") {
		t.Error("no full-width class without widgets")
	}
	for _, f := range []url.Values{{"type": {"text"}, "sidebar": {"sidebar-1"}}, {"type": {"search"}, "sidebar": {"sidebar-1"}}} {
		if res := post(e, "/admin/widgets/add", admin, f); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("add: %d", res.StatusCode)
		}
	}
	if res := post(e, "/admin/widgets/add", admin, url.Values{"type": {"evil"}, "sidebar": {"sidebar-1"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown type: %d", res.StatusCode)
	}
	if res := post(e, "/admin/widgets/add", admin, url.Values{"type": {"text"}, "sidebar": {"footer"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown area: %d", res.StatusCode)
	}
	if res := post(e, "/admin/widgets/text-2", admin, url.Values{"title": {"About <i>me</i>"}, "text": {"<p>Hi <b>there</b></p>"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	body := e.do("GET", "/hello/", nil, nil, nil).Body.String()
	if !strings.Contains(body, `<aside id="text-2" class="widget widget_text"><h3 class="widget-title">About me</h3>`) ||
		!strings.Contains(body, "<p>Hi <b>there</b></p>") || !strings.Contains(body, `id="search-2"`) || strings.Contains(body, "full-width") {
		t.Errorf("sidebar: %s", body[strings.Index(body, `id="secondary"`):])
	}
	// Reorder: search first, text inactive.
	rec := doJSON(e, "/admin/widgets/order", admin, `{"sidebar-1":["search-2"],"wp_inactive_widgets":["text-2"]}`)
	if rec.Code != 200 {
		t.Fatalf("order: %d %s", rec.Code, rec.Body)
	}
	body = e.do("GET", "/hello/", nil, nil, nil).Body.String()
	if strings.Contains(body, "text-2") || !strings.Contains(body, "search-2") {
		t.Error("inactive widget still shown")
	}
	if rec := doJSON(e, "/admin/widgets/order", admin, `{"sidebar-1":["nope-9"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown widget: %d", rec.Code)
	}
	if rec := doJSON(e, "/admin/widgets/order", admin, `{"evil":["search-2"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown area: %d", rec.Code)
	}
	if res := post(e, "/admin/widgets/search-2/delete", admin, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if n, _ := db.WidgetCount(ctx, e.d.R, "sidebar-1"); n != 0 {
		t.Errorf("widgets left: %d", n)
	}
}

func doJSON(e *testEnv, path string, cur *auth.Current, body string) *httptestRecorder {
	r := newJSONRequest(path, body)
	r.Header.Set("Origin", home)
	r.Header.Set("X-CSRF-Token", cur.CSRF)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cur.Session.UserAgent})
	rec := newRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

type httptestRecorder = httptest.ResponseRecorder

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func newJSONRequest(path, body string) *http.Request {
	r := httptest.NewRequest("POST", home+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	return r
}
