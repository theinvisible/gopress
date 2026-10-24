package plugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const validManifest = `{
	"name": "hello", "version": "1.2.0", "title": "Hello", "api": 1,
	"permissions": {
		"http": ["api.example.org"], "routes": ["/ping", "/api/*"],
		"legacy_routes": [{"query": {"hello_ping": "1"}, "route": "/ping"}],
		"hooks": ["the_content", "comment_pre_save"], "data": ["posts:read", "import:post:hello_item"],
		"frontend_assets": ["assets/hello.css", "assets/hello.js"]
	},
	"shortcodes": [{"name": "hello", "assets": ["assets/hello.css"]}],
	"widgets": [{"type": "hello_widget", "title": "Hello", "fields": [{"name": "title", "label": "Title"}]}],
	"admin": [
		{"id": "settings", "title": "Hello", "menu": "settings", "capability": "manage_options", "kind": "settings",
		 "fields": [{"name": "greeting", "label": "Greeting", "max_length": 100}]},
		{"id": "app", "title": "Hello app", "menu": "plugin", "capability": "edit_posts", "kind": "iframe", "file": "admin/app.html"}
	],
	"editor": [{"id": "insert", "label": "Insert hello", "shortcode": "hello", "page": "app"}],
	"dashboard": [{"id": "stats", "title": "Hello stats", "capability": "manage_options"}]
}`

func mutate(t *testing.T, f func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validManifest), &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, _ := json.Marshal(m)
	return b
}

func perms(m map[string]any) map[string]any { return m["permissions"].(map[string]any) }

func TestManifest(t *testing.T) {
	if _, err := ParseManifest([]byte(validManifest)); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	bad := map[string]func(m map[string]any){
		"unknown key":    func(m map[string]any) { m["permisions"] = map[string]any{} },
		"name upper":     func(m map[string]any) { m["name"] = "Hello" },
		"name path":      func(m map[string]any) { m["name"] = "../x" },
		"version":        func(m map[string]any) { m["version"] = "1.0" },
		"api":            func(m map[string]any) { m["api"] = 2 },
		"title control":  func(m map[string]any) { m["title"] = "a\nb" },
		"http url":       func(m map[string]any) { perms(m)["http"] = []any{"https://x.org"} },
		"http ip":        func(m map[string]any) { perms(m)["http"] = []any{"10.0.0.1"} },
		"http localhost": func(m map[string]any) { perms(m)["http"] = []any{"localhost"} },
		"http port":      func(m map[string]any) { perms(m)["http"] = []any{"x.org:8080"} },
		"http internal":  func(m map[string]any) { perms(m)["http"] = []any{"db.internal"} },
		"route dotdot":   func(m map[string]any) { perms(m)["routes"] = []any{"/../admin"} },
		"route no slash": func(m map[string]any) { perms(m)["routes"] = []any{"ping"} },
		"legacy core var": func(m map[string]any) {
			perms(m)["legacy_routes"] = []any{map[string]any{"query": map[string]any{"p": "1"}, "route": "/ping"}}
		},
		"legacy unknown route": func(m map[string]any) {
			perms(m)["legacy_routes"] = []any{map[string]any{"query": map[string]any{"x": "1"}, "route": "/nope"}}
		},
		"hook":           func(m map[string]any) { perms(m)["hooks"] = []any{"init"} },
		"data":           func(m map[string]any) { perms(m)["data"] = []any{"users:read"} },
		"asset html":     func(m map[string]any) { perms(m)["frontend_assets"] = []any{"assets/x.html"} },
		"asset outside":  func(m map[string]any) { perms(m)["frontend_assets"] = []any{"admin/x.js"} },
		"asset dotdot":   func(m map[string]any) { perms(m)["frontend_assets"] = []any{"assets/../plugin.js"} },
		"duplicate perm": func(m map[string]any) { perms(m)["hooks"] = []any{"head", "head"} },
		"core shortcode": func(m map[string]any) { m["shortcodes"] = []any{map[string]any{"name": "gallery"}} },
		"undeclared asset": func(m map[string]any) {
			m["shortcodes"] = []any{map[string]any{"name": "hello", "assets": []any{"assets/other.js"}}}
		},
		"admin capability": func(m map[string]any) { m["admin"].([]any)[0].(map[string]any)["capability"] = "read" },
		"iframe file":      func(m map[string]any) { m["admin"].([]any)[1].(map[string]any)["file"] = "assets/app.html" },
		"field type": func(m map[string]any) {
			m["widgets"].([]any)[0].(map[string]any)["fields"] = []any{map[string]any{"name": "x", "label": "X", "type": "html"}}
		},
		"insert other code": func(m map[string]any) { m["editor"].([]any)[0].(map[string]any)["shortcode"] = "gallery" },
		"insert no iframe":  func(m map[string]any) { m["editor"].([]any)[0].(map[string]any)["page"] = "settings" },
	}
	for name, f := range bad {
		if _, err := ParseManifest(mutate(t, f)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPermissionCover(t *testing.T) {
	m, _ := ParseManifest([]byte(validManifest))
	approved, _ := ParsePermissions(m.Permissions.Canonical())
	if ok, extra := m.Permissions.Covers(approved); !ok {
		t.Fatalf("own permissions not covered: %v", extra)
	}
	more := m.Permissions
	more.HTTP = append([]string{"evil.example"}, more.HTTP...)
	more.LegacyRoutes = append(more.LegacyRoutes, LegacyRoute{Query: map[string]string{"y": "2"}, Route: "/ping"})
	if ok, extra := more.Covers(approved); ok || len(extra) != 2 {
		t.Fatalf("extension not detected: %v", extra)
	}
	if len(m.Permissions.Describe()) < 8 {
		t.Error("describe")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.0.0", "1.0.0", 0}, {"1.0.10", "1.0.9", 1}, {"1.0.0-rc.1", "1.0.0", -1}, {"2.0.0", "10.0.0", -1}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %d", c.a, c.b, got)
		}
	}
}

type zipFile struct {
	name string
	data []byte
	mode fs.FileMode
}

func makeZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: time.Unix(0, 0)}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(f.data)
	}
	w.Close()
	return b.Bytes()
}

var fakeWasm = append(append([]byte{}, wasmMagic...), 0)

func validFiles() []zipFile {
	return []zipFile{{name: "plugin.json", data: []byte(validManifest)}, {name: "plugin.wasm", data: fakeWasm},
		{name: "assets/hello.css", data: []byte("b{}")}, {name: "assets/hello.js", data: []byte("1")},
		{name: "admin/app.html", data: []byte("<p>")}, {name: "README.md", data: []byte("hi")}}
}

func TestReadZip(t *testing.T) {
	p, err := ReadZip(makeZip(t, validFiles()...), "uploaded")
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := p.File("assets/hello.js"); !ok || string(b) != "1" {
		t.Error("asset missing")
	}
	if _, ok := p.File("README.md"); ok {
		t.Error("root file exposed as asset")
	}
	with := func(extra ...zipFile) []byte { return makeZip(t, append(validFiles(), extra...)...) }
	without := func(name string) []byte {
		var fs []zipFile
		for _, f := range validFiles() {
			if f.name != name {
				fs = append(fs, f)
			}
		}
		return makeZip(t, fs...)
	}
	bad := map[string][]byte{
		"not a zip":       []byte("hello"),
		"zip slip":        with(zipFile{name: "../evil.js", data: []byte("x")}),
		"absolute":        with(zipFile{name: "/etc/x.js", data: []byte("x")}),
		"backslash":       with(zipFile{name: `assets\..\x.js`, data: []byte("x")}),
		"symlink":         with(zipFile{name: "assets/link.js", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}),
		"html in assets":  with(zipFile{name: "assets/x.html", data: []byte("<script>")}),
		"exe":             with(zipFile{name: "assets/x.exe", data: []byte("MZ")}),
		"unknown root":    with(zipFile{name: "x.js", data: []byte("1")}),
		"case collision":  with(zipFile{name: "assets/HELLO.css", data: []byte("x")}),
		"no manifest":     without("plugin.json"),
		"no wasm":         without("plugin.wasm"),
		"missing asset":   without("assets/hello.js"),
		"missing iframe":  without("admin/app.html"),
		"wasm magic":      makeZip(t, zipFile{name: "plugin.json", data: []byte(validManifest)}, zipFile{name: "plugin.wasm", data: []byte("MZ\x90")}),
		"too large":       append(make([]byte, MaxZipBytes), 1),
		"bomb":            with(zipFile{name: "assets/big.json", data: bytes.Repeat([]byte{' '}, maxFileBytes+1)}),
		"too many":        manyFiles(t),
		"bad manifest":    makeZip(t, zipFile{name: "plugin.json", data: []byte(`{"name":"x"}`)}, zipFile{name: "plugin.wasm", data: fakeWasm}),
		"dir traversal 2": with(zipFile{name: "assets/a/../../b.js", data: []byte("1")}),
	}
	for name, data := range bad {
		if _, err := ReadZip(data, "uploaded"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func manyFiles(t *testing.T) []byte {
	files := validFiles()
	for i := range maxEntries {
		files = append(files, zipFile{name: "assets/f" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + ".css", data: []byte("x")})
	}
	return makeZip(t, files...)
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	bundledZip := makeZip(t, validFiles()...)
	bundled := fstest.MapFS{"hello.zip": {Data: bundledZip}}
	s, err := OpenStore(dir, bundled, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p := s.Get("hello"); p == nil || p.Source != "bundled" {
		t.Fatal("bundled plugin missing")
	}
	// An older upload is refused, a newer one wins and survives a restart.
	older := strings.Replace(validManifest, `"1.2.0"`, `"1.1.0"`, 1)
	files := validFiles()
	files[0].data = []byte(older)
	if _, err := s.Install(makeZip(t, files...)); err != ErrOlder {
		t.Fatalf("older upload: %v", err)
	}
	files[0].data = []byte(strings.Replace(validManifest, `"1.2.0"`, `"1.3.0"`, 1))
	if _, err := s.Install(makeZip(t, files...)); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, bundled, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := s2.Get("hello"); p.Source != "uploaded" || p.Manifest.Version != "1.3.0" {
		t.Fatalf("after restart: %s %s", p.Source, p.Manifest.Version)
	}
	if err := s2.Remove("hello"); err != nil {
		t.Fatal(err)
	}
	if p := s2.Get("hello"); p.Source != "bundled" {
		t.Fatal("bundled version not back")
	}
	if _, err := os.Stat(filepath.Join(dir, "hello.zip")); !os.IsNotExist(err) {
		t.Fatal("upload not deleted")
	}
	// A corrupt upload on disk is skipped, not fatal.
	os.WriteFile(filepath.Join(dir, "broken.zip"), []byte("x"), 0o600)
	if _, err := OpenStore(dir, bundled, nil); err != nil {
		t.Fatal(err)
	}
}
