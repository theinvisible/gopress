package theme

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/themes"
)

func TestLoadAndRender(t *testing.T) {
	th, err := Load(themes.FS, "twentytwelve")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	v := &View{Lang: "en-US", Charset: "UTF-8", Title: "T", SiteName: "S", HomeURL: "/",
		Posts: []*Post{{ID: 1, Class: "post", Title: template.HTML("Hello"), URL: "/hello/", Content: "<p>x</p>"}}}
	if err := th.Render(&buf, "index", v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `<a href="/hello/" rel="bookmark">Hello</a>`) {
		t.Errorf("unexpected output:\n%s", buf.String())
	}
	if p := th.AssetPath("style.css"); !strings.HasPrefix(p, "/static/twentytwelve/") || strings.Contains(p, "missing") {
		t.Errorf("AssetPath = %q", p)
	}
}
