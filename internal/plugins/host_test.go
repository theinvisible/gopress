package plugins

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/plugins/plugintest"
	"github.com/theinvisible/gopress/internal/site"
	"github.com/theinvisible/gopress/internal/theme"
	"github.com/theinvisible/gopress/themes"
)

// testerPackage builds and packs testdata/tester.
func testerPackage(t *testing.T) []byte {
	b, err := PackDir(plugintest.Tester(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type hostEnv struct {
	t    *testing.T
	d    *db.DB
	site *site.Site
	h    *Host
	srv  *httptest.Server
}

func newHostEnv(t *testing.T) *hostEnv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for k, v := range map[string]any{"blogname": "Test", "home": "https://example.org", "permalink_structure": "/%postname%/",
		"timezone_string": "UTC"} {
		db.SetOption(ctx, d.W, k, v)
	}
	now := db.Time{Time: time.Now().UTC()}
	db.InsertPost(ctx, d.W, &db.Post{ID: 10, Type: db.TypePost, Status: db.StatusPublish, Slug: "hello", Title: "Hello",
		Content: "<p>Hi</p>", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 12, Type: db.TypePost, Status: db.StatusPublish, Slug: "members", Title: "Members",
		Content: "<p>secretword</p>", Password: "pw", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 11, Type: db.TypePost, Status: db.StatusDraft, Title: "Draft", Content: "secret",
		Date: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 20, Type: db.TypeAttachment, Status: db.StatusInherit, ParentID: 10, Title: "pic",
		MimeType: "image/png", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertMedia(ctx, d.W, &db.Media{PostID: 20, File: "2026/10/pic.png", Mime: "image/png", Width: 10, Height: 10})
	db.InsertPost(ctx, d.W, &db.Post{ID: 21, Type: db.TypeAttachment, Status: db.StatusInherit, ParentID: 11, Title: "secret pic",
		MimeType: "image/png", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertMedia(ctx, d.W, &db.Media{PostID: 21, File: "2026/10/secret.png", Mime: "image/png", Width: 10, Height: 10})
	db.InsertComment(ctx, d.W, &db.Comment{ID: 1, PostID: 10, Author: "A", Content: "hi", Status: db.CommentApproved, Type: "comment"})
	db.Stash(ctx, d.W, "wordpress", "post:tester_item", "7", map[string]string{"title": "seven"})
	db.Stash(ctx, d.W, "wordpress", "post:other", "8", map[string]string{"title": "not yours"})

	th, err := theme.Load(themes.FS, "twentytwelve")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("GP_TEST_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	s := site.New(d, th, site.Options{BaseURL: "https://example.org", Secret: []byte("k"), Log: log})
	store, err := OpenStore(t.TempDir(), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	e := &hostEnv{t: t, d: d, site: s}
	// The API server of the test plugin: every connection goes to it.
	e.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "https://evil.example/", http.StatusFound)
		case "/big":
			w.Write(bytes.Repeat([]byte("z"), maxHTTPResponse+10))
		default:
			w.Write([]byte("api says " + r.Method + " " + r.Header.Get("X-Test")))
		}
	}))
	t.Cleanup(e.srv.Close)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, e.srv.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	e.h, err = New(ctx, Options{DB: d, Store: store, Site: s, Log: log, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.h.Close(context.Background()) })
	if _, _, err := e.h.Install(ctx, testerPackage(t)); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Activate(ctx, "tester", ""); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *hostEnv) shortcode(tag string, attrs map[string]string) (RenderOut, error) {
	var out RenderOut
	err := e.h.call(context.Background(), "tester", ExportShortcode, callRender, ShortcodeIn{Tag: tag, Attrs: attrs}, &out)
	return out, err
}

func TestRuntimeBasics(t *testing.T) {
	e := newHostEnv(t)
	ctx := context.Background()
	out, err := e.shortcode("hello", map[string]string{"name": "Ti"})
	if err != nil || out.HTML != `<span class="hello" data-n="1">Hello, Ti!</span>` || len(out.Assets) != 2 {
		t.Fatalf("hello: %v %+v", err, out)
	}
	out, _ = e.shortcode("hello", nil)
	if !strings.Contains(out.HTML, `data-n="2"`) {
		t.Errorf("counter in KV: %s", out.HTML)
	}
	if err := e.h.SaveSettings(ctx, "tester", "settings", map[string]any{"greeting": "Servus", "count": 2.0, "images": []any{}}); err != nil {
		t.Fatal(err)
	}
	if out, _ = e.shortcode("hello", nil); !strings.Contains(out.HTML, "Servus, world") {
		t.Errorf("settings: %s", out.HTML)
	}
	// A plugin error is reported, the instance stays usable.
	if _, err := e.shortcode("fail", nil); err == nil || !strings.Contains(err.Error(), "deliberate failure") {
		t.Errorf("fail: %v", err)
	}
	// Parallel calls.
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.shortcode("hello", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestRuntimeLimits(t *testing.T) {
	e := newHostEnv(t)
	start := time.Now()
	if _, err := e.shortcode("loop", nil); err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Errorf("endless loop: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("loop stopped after %s", d)
	}
	if _, err := e.shortcode("panic", nil); err == nil {
		t.Error("panic not reported")
	}
	if _, err := e.shortcode("alloc", nil); err == nil {
		t.Error("200 MB allocation succeeded")
	}
	if _, err := e.shortcode("big", nil); err == nil || !strings.Contains(err.Error(), "output too large") {
		t.Errorf("big output: %v", err)
	}
	// A failure the plugin reports itself does not count; five crashes
	// within a minute pause the plugin.
	e.shortcode("fail", nil)
	if _, err := e.shortcode("hello", nil); err != nil {
		t.Errorf("paused by a reported error: %v", err)
	}
	e.shortcode("panic", nil)
	if _, err := e.shortcode("hello", nil); err != ErrUnavailable {
		t.Errorf("not paused after 5 failures: %v", err)
	}
	if st := e.h.Status("tester"); !st.Paused || st.LastError == "" {
		t.Errorf("status: %+v", st)
	}
	p := e.h.get("tester")
	p.mu.Lock()
	p.paused = time.Time{}
	p.mu.Unlock()
	if out, err := e.shortcode("hello", nil); err != nil || !strings.Contains(out.HTML, "Hello") {
		t.Errorf("after the pause: %v", err)
	}
}

func (e *hostEnv) op(attrs map[string]string) string {
	e.t.Helper()
	out, err := e.shortcode("op", attrs)
	if err != nil {
		e.t.Fatalf("op %v: %v", attrs, err)
	}
	return out.HTML
}

func TestHostFunctions(t *testing.T) {
	e := newHostEnv(t)
	ctx := context.Background()
	if got := e.op(map[string]string{"do": "post"}); !strings.Contains(got, `"title":"Hello"`) {
		t.Errorf("post: %s", got)
	}
	if got := e.op(map[string]string{"do": "posts"}); strings.Contains(got, "Draft") || !strings.Contains(got, "Hello") {
		t.Errorf("posts: %s", got)
	}
	if got := e.op(map[string]string{"do": "media"}); !strings.Contains(got, "pic.png") || strings.Contains(got, "secret") {
		t.Errorf("media of a draft leaked: %s", got)
	}
	if got := e.op(map[string]string{"do": "spam"}); got != "null" {
		t.Errorf("set status: %s", got)
	}
	if c, _ := db.GetComment(ctx, e.d.R, 1); c.Status != db.CommentSpam {
		t.Errorf("comment status %s", c.Status)
	}
	if got := e.op(map[string]string{"do": "stash", "kind": "post:tester_item"}); !strings.Contains(got, "seven") {
		t.Errorf("stash: %s", got)
	}
	if got := e.op(map[string]string{"do": "stash", "kind": "post:other"}); !strings.Contains(got, "permission denied") {
		t.Errorf("foreign stash: %s", got)
	}
	if got := e.op(map[string]string{"do": "kvbad"}); !strings.Contains(got, "invalid collection") {
		t.Errorf("plugin wrote its admin settings: %s", got)
	}
	if got := e.op(map[string]string{"do": "site"}); !strings.Contains(got, `"url":"https://example.org/"`) {
		t.Errorf("site: %s", got)
	}
	// HTTP: only approved hosts, https, no redirects elsewhere, no
	// oversized answers or forbidden headers.
	api := "https://api.example.org"
	if got := e.op(map[string]string{"do": "fetch", "url": api + "/x"}); !strings.Contains(got, "api says GET 1") {
		t.Errorf("fetch: %s", got)
	}
	for url, want := range map[string]string{
		"https://evil.example/":         "not allowed",
		"http://api.example.org/":       "not allowed",
		"https://api.example.org:8443/": "not allowed",
		"https://u:p@api.example.org/":  "not allowed",
		api + "/redirect":               "not allowed",
		api + "/big":                    "larger than",
	} {
		if got := e.op(map[string]string{"do": "fetch", "url": url}); !strings.Contains(got, want) {
			t.Errorf("fetch %s: %s", url, got)
		}
	}
	if got := e.op(map[string]string{"do": "fetch", "url": api, "hname": "Host", "hvalue": "x"}); !strings.Contains(got, "not allowed") {
		t.Errorf("Host header: %s", got)
	}
	// Storage quota (50 MB): 900 KB values until it is full.
	var full string
	for i := range 60 {
		if got := e.op(map[string]string{"do": "kvbig", "key": strings.Repeat("k", i+1)}); got != "null" {
			full = got
			break
		}
	}
	if !strings.Contains(full, "quota") {
		t.Errorf("quota: %q", full)
	}
}

func TestHostFunctionPermissions(t *testing.T) {
	e := newHostEnv(t)
	ctx := context.Background()
	// Remove the data permissions: activation with the narrower manifest.
	pkg := e.h.Package("tester")
	pkg.Manifest.Permissions.Data = nil
	pkg.Manifest.Permissions.HTTP = nil
	if err := e.h.Activate(ctx, "tester", ""); err != nil {
		t.Fatal(err)
	}
	for _, do := range []string{"post", "posts", "media", "comment", "spam", "fetch"} {
		if got := e.op(map[string]string{"do": do, "url": "https://api.example.org/"}); !strings.Contains(got, "permission denied") &&
			!strings.Contains(got, "not allowed") {
			t.Errorf("%s without permission: %s", do, got)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{"93.184.216.34": true, "2606:4700::1111": true, "127.0.0.1": false, "10.1.2.3": false,
		"172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"64:ff9b::a00:1": false, "224.0.0.1": false, "255.255.255.255": false} {
		if got := publicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("publicAddr(%s) = %v", s, got)
		}
	}
	if dialControl("tcp", "93.184.216.34:443", nil) != nil || dialControl("tcp", "93.184.216.34:22", nil) == nil ||
		dialControl("tcp", "127.0.0.1:443", nil) == nil {
		t.Error("dialControl")
	}
}

// Regression tests for the M3 security review.
func TestReviewFindings(t *testing.T) {
	e := newHostEnv(t)
	ctx := context.Background()
	// Load is not a fault: many slow calls at once queue, they do not
	// pause the plugin.
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() { defer wg.Done(); e.shortcode("slow", nil) }()
	}
	wg.Wait()
	if st := e.h.Status("tester"); st.Paused {
		t.Errorf("paused by load: %+v", st)
	}
	// Password protected posts are neither listed nor counted.
	if got := e.op(map[string]string{"do": "posts", "search": "secretword"}); !strings.Contains(got, `"total":0`) {
		t.Errorf("password protected post found: %s", got)
	}
	if got := e.op(map[string]string{"do": "kvlist", "collection": "_settings"}); !strings.Contains(got, "invalid collection") {
		t.Errorf("_settings listed: %s", got)
	}
	if err := e.h.Activate(ctx, "tester", strings.Repeat("0", 64)); err != ErrChanged {
		t.Errorf("activation of another package: %v", err)
	}
}

func TestCheckModule(t *testing.T) {
	if err := checkModule(e2eWasm(t)); err != nil {
		t.Fatalf("Go plugin refused: %v", err)
	}
	huge := append(append([]byte{}, wasmMagic...), 4, 7, 1, 0x70, 0, 0x80, 0x80, 0x80, 0x40) // one table, min 2^27
	if err := checkModule(huge); err == nil || !strings.Contains(err.Error(), "table") {
		t.Errorf("huge table: %v", err)
	}
	imported := append(append([]byte{}, wasmMagic...), 2, 12, 1, 1, 'm', 1, 't', 1, 0x70, 0, 0x80, 0x80, 0x80, 0x40)
	if err := checkModule(imported); err == nil {
		t.Error("huge imported table accepted")
	}
	for _, bad := range [][]byte{wasmMagic[:4], append(append([]byte{}, wasmMagic...), 4, 200)} {
		if checkModule(bad) == nil {
			t.Errorf("malformed module %x accepted", bad)
		}
	}
}

func e2eWasm(t *testing.T) []byte {
	_, wasm := plugintest.Tester(t)
	b, err := os.ReadFile(wasm)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
