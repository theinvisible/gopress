package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/plugins"
	"github.com/theinvisible/gopress/internal/plugins/plugintest"
)

var reSHA = regexp.MustCompile(`name="sha256" value="([0-9a-f]{64})"`)

// activateTester approves and activates the test plugin as admin.
func (e *testEnv) activateTester(admin *auth.Current) {
	e.t.Helper()
	page := e.do("GET", "/admin/plugins/tester/activate", admin, nil, nil).Body.String()
	m := reSHA.FindStringSubmatch(page)
	if m == nil {
		e.t.Fatalf("review page without sha256")
	}
	if res := post(e, "/admin/plugins/tester/activate", admin, url.Values{"sha256": {m[1]}}); res.StatusCode != http.StatusSeeOther ||
		!strings.Contains(res.Header.Get("Location"), "msg=activated") {
		e.t.Fatalf("activate: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestPluginsScreen(t *testing.T) {
	e := newEnvWith(t, true)
	ctx := context.Background()
	db.Stash(ctx, e.d.W, "wordpress", "post:tester_item", "7", map[string]string{"title": "seven"})
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	editor := e.session(auth.RoleEditor, db.SessionFull)
	if rec := e.do("GET", "/admin/plugins/", editor, nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("editor sees plugins: %d", rec.Code)
	}
	page := e.do("GET", "/admin/plugins/", admin, nil, nil).Body.String()
	if !strings.Contains(page, "Tester") || !strings.Contains(page, "Needs approval") && !strings.Contains(page, "Activate") {
		t.Fatal("plugin list")
	}
	review := e.do("GET", "/admin/plugins/tester/activate", admin, nil, nil).Body.String()
	for _, want := range []string{"Send HTTPS requests to api.example.org", "Change the status of comments", "JavaScript (runs in visitors",
		"Read imported WordPress data (post:tester_item)", "<code>[hello]</code>", "custom page (sandboxed)"} {
		if !strings.Contains(review, want) {
			t.Errorf("review lacks %q", want)
		}
	}
	// Activation needs a recent authentication and the reviewed package.
	db.SetSessionReauth(ctx, e.d.W, admin.Session.IDHash, 0)
	sha := reSHA.FindStringSubmatch(review)[1]
	if res := post(e, "/admin/plugins/tester/activate", admin, url.Values{"sha256": {sha}}); !strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Fatalf("activate without reauth: %s", res.Header.Get("Location"))
	}
	e.auth.MarkReauth(ctx, admin)
	if res := post(e, "/admin/plugins/tester/activate", admin, url.Values{"sha256": {strings.Repeat("0", 64)}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("activate another package: %d", res.StatusCode)
	}
	e.activateTester(admin)
	// The WordPress import ran (synchronously in tests).
	if v, ok, _ := db.KVGet(ctx, e.d.R, "tester", "items", "7"); !ok || !strings.Contains(v, "seven") {
		t.Errorf("import: %q", v)
	}

	// Menu entries of the plugin pages, by capability.
	dash := e.do("GET", "/admin/", admin, nil, nil).Body.String()
	for _, want := range []string{`href="/admin/plugin/tester/settings"`, `href="/admin/plugin/tester/items"`, `href="/admin/plugin/tester/app"`,
		"Tester stats", "<p>stats for administrator</p>"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if strings.Contains(dash, "<script>x</script>") {
		t.Error("script in a dashboard box")
	}
	if d := e.do("GET", "/admin/", editor, nil, nil).Body.String(); strings.Contains(d, "/admin/plugin/tester/settings") ||
		!strings.Contains(d, "/admin/plugin/tester/items") || strings.Contains(d, "Tester stats") {
		t.Error("editor menu/dashboard by capability")
	}
	if rec := e.do("GET", "/admin/plugin/tester/settings", editor, nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("editor opens the settings: %d", rec.Code)
	}

	// Settings form: validated against the manifest.
	if res := post(e, "/admin/plugin/tester/settings", admin, url.Values{"greeting": {"Hi"}, "count": {"99"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("out of range: %d", res.StatusCode)
	}
	if res := post(e, "/admin/plugin/tester/settings", admin, url.Values{"greeting": {"Servus"}, "count": {"4"}, "images": {"5,6"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	set, _ := e.plugins.Settings(ctx, "tester", "settings")
	if set["greeting"] != "Servus" || set["count"] != 4.0 || len(set["images"].([]any)) != 2 {
		t.Errorf("settings: %v", set)
	}

	// Table: cells are text, actions reach the plugin.
	tbl := e.do("GET", "/admin/plugin/tester/items", editor, nil, nil).Body.String()
	if !strings.Contains(tbl, "&lt;b&gt;One&lt;/b&gt;") {
		t.Error("table cell not escaped")
	}
	res := post(e, "/admin/plugin/tester/items", editor, url.Values{"action": {"delete"}, "ids[]": {"1"}})
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "delete") || !strings.Contains(loc, "editor") {
		t.Errorf("table action: %d %s", res.StatusCode, loc)
	}

	// Iframe page and its bridge API.
	frame := e.do("GET", "/admin/plugin/tester/app", admin, nil, nil).Body.String()
	if !strings.Contains(frame, `sandbox="allow-scripts allow-forms allow-popups allow-modals"`) || strings.Contains(frame, "allow-same-origin") {
		t.Error("iframe sandbox")
	}
	api := func(cur *auth.Current, body string, hdr map[string]string) (int, string) {
		r := e.raw("POST", "/admin/api/plugins/tester/app", cur, body, hdr)
		return r.Code, r.Body.String()
	}
	if code, body := api(admin, `{"method":"echo","params":{"a":1}}`, map[string]string{"X-CSRF-Token": admin.CSRF, "Origin": home}); code != 200 ||
		!strings.Contains(body, `"method":"echo"`) || !strings.Contains(body, `"user":"administrator"`) {
		t.Errorf("api: %d %s", code, body)
	}
	if code, _ := api(admin, `{"method":"echo"}`, map[string]string{"Origin": home}); code != http.StatusForbidden {
		t.Errorf("api without CSRF: %d", code)
	}
	if code, _ := api(editor, `{"method":"echo"}`, map[string]string{"X-CSRF-Token": editor.CSRF, "Origin": home}); code != http.StatusForbidden {
		t.Errorf("api by an editor: %d", code)
	}
	ui := e.do("GET", "/plugin-ui/tester/app.html", nil, nil, nil)
	if ui.Code != 200 || !strings.Contains(ui.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") ||
		strings.Contains(ui.Header().Get("Content-Security-Policy"), "allow-same-origin") || ui.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("ui file: %d %v", ui.Code, ui.Header())
	}
	for _, p := range []string{"/plugin-ui/tester/../plugin.json", "/plugin-ui/tester/plugin.wasm", "/plugin-ui/nope/app.html"} {
		if rec := e.do("GET", p, nil, nil, nil); rec.Code == 200 {
			t.Errorf("%s served", p)
		}
	}
	if rec := e.do("GET", "/plugin-ui/_frame.js", nil, nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "insertShortcode") {
		t.Errorf("frame script: %d", rec.Code)
	}

	// Widgets screen and editor know the plugin's types and buttons.
	if w := e.do("GET", "/admin/widgets", admin, nil, nil).Body.String(); !strings.Contains(w, "tester_widget") {
		t.Error("plugin widget type missing")
	}
	if ed := e.do("GET", "/admin/posts/new", admin, nil, nil).Body.String(); !strings.Contains(ed, "tester-insert") {
		t.Error("editor insert missing")
	}

	// Deactivate: pages disappear.
	if res := post(e, "/admin/plugins/tester/deactivate", admin, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("deactivate: %d", res.StatusCode)
	}
	if rec := e.do("GET", "/admin/plugin/tester/settings", admin, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("page of an inactive plugin: %d", rec.Code)
	}
	if rec := e.do("GET", "/plugin-ui/tester/app.html", nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("ui of an inactive plugin: %d", rec.Code)
	}
}

func TestPluginUpload(t *testing.T) {
	e := newEnvWith(t, true)
	ctx := context.Background()
	admin := e.adminWithReauth()
	e.activateTester(admin)
	upload := func(data []byte) *http.Response {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		w.WriteField("_csrf", admin.CSRF)
		f, _ := w.CreateFormFile("plugin", "p.zip")
		f.Write(data)
		w.Close()
		return e.raw("POST", "/admin/plugins/upload", admin, b.String(), map[string]string{"Content-Type": w.FormDataContentType(), "Origin": home}).Result()
	}
	if res := upload([]byte("not a zip")); res.StatusCode != http.StatusBadRequest {
		t.Errorf("garbage upload: %d", res.StatusCode)
	}
	// A new version asking for more is installed but stays inactive.
	dir, wasm := plugintest.Tester(t)
	zip := mustRepack(t, dir, wasm, func(m map[string]any) {
		m["version"] = "1.1.0"
		m["permissions"].(map[string]any)["http"] = []any{"api.example.org", "other.example.org"}
	})
	res := upload(zip)
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "/admin/plugins/tester/activate") {
		t.Fatalf("update with more permissions: %d %s", res.StatusCode, loc)
	}
	if st := e.plugins.Status("tester"); st.Running {
		t.Error("plugin kept running with unapproved permissions")
	}
	review := e.do("GET", "/admin/plugins/tester/activate", admin, nil, nil).Body.String()
	if !strings.Contains(review, "other.example.org") || !strings.Contains(review, "New") {
		t.Error("new permission not highlighted")
	}
	e.activateTester(admin)
	if st := e.plugins.Status("tester"); !st.Running {
		t.Error("not running after approval")
	}
	// Delete (uploaded): with its data.
	if res := post(e, "/admin/plugins/tester/delete", admin, url.Values{"delete_data": {"1"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if list, _ := e.plugins.List(ctx); len(list) != 0 {
		t.Error("plugin still listed")
	}
	time.Sleep(10 * time.Millisecond)
}

func mustRepack(t *testing.T, dir, wasm string, edit func(map[string]any)) []byte {
	t.Helper()
	tmp := t.TempDir()
	copyDir(t, dir, tmp)
	var m map[string]any
	b := readFile(t, tmp+"/plugin.json")
	json.Unmarshal(b, &m)
	edit(m)
	nb, _ := json.Marshal(m)
	writeFile(t, tmp+"/plugin.json", nb)
	zip, err := plugins.PackDir(tmp, wasm)
	if err != nil {
		t.Fatal(err)
	}
	return zip
}

// raw sends a request with a raw body.
func (e *testEnv) raw(method, target string, cur *auth.Current, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, home+target, strings.NewReader(body))
	if cur != nil {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cur.Session.UserAgent})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
