package main

import (
	"encoding/json"
	"strings"
	"testing"

	gp "github.com/theinvisible/gopress/pdk/go"
	"github.com/theinvisible/gopress/pdk/go/fakehost"
)

func img(id int64, caption string) gp.Media {
	u := "https://example.org/wp-content/uploads/2026/07/p" + string(rune('0'+id%10))
	return gp.Media{ID: id, URL: u + ".png", Mime: "image/png", Width: 1440, Height: 900, Alt: "alt " + caption, Caption: caption,
		Sizes: map[string]gp.Size{
			"thumbnail": {URL: u + "-150x150.png", Width: 150, Height: 150},
			"medium":    {URL: u + "-300x188.png", Width: 300, Height: 188},
			"large":     {URL: u + "-1024x640.png", Width: 1024, Height: 640},
		}}
}

func newHost(t *testing.T) *fakehost.Host {
	h := fakehost.New()
	h.Media[1] = img(1, "First <b>photo</b> & more")
	h.Media[2] = img(2, "")
	h.Media[3] = gp.Media{ID: 3, URL: "https://example.org/doc.pdf", Mime: "application/pdf"}
	h.Install()
	return h
}

func render(t *testing.T, attrs map[string]string) gp.Render {
	t.Helper()
	var out gp.Render
	if err := gp.CallExport("render_shortcode", gp.ShortcodeRequest{Tag: "fotogalerie", Attrs: attrs}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestShortcode(t *testing.T) {
	h := newHost(t)
	h.Put("galleries", "791", Gallery{ID: 791, Title: "Trip", Images: []int64{2, 1, 3, 99}, Captions: true})
	out := render(t, map[string]string{"id": "791"})
	if len(out.Assets) != 4 {
		t.Errorf("assets: %v", out.Assets)
	}
	for _, want := range []string{
		`<div class="fslb-gallery fslb-gallery--shortcode" data-captions="1"`,
		`<div class="fslb-slider swiper" id="fslb-791">`,
		`data-fslb-index="0" data-full="https://example.org/wp-content/uploads/2026/07/p2.png"`,
		`data-fslb-index="1"`,
		`src="https://example.org/wp-content/uploads/2026/07/p1-1024x640.png" width="1024" height="640"`,
		`srcset="https://example.org/wp-content/uploads/2026/07/p1-300x188.png 300w, https://example.org/wp-content/uploads/2026/07/p1-1024x640.png 1024w, https://example.org/wp-content/uploads/2026/07/p1.png 1440w"`,
		`sizes="(max-width: 1024px) 100vw, 1024px"`,
		`<figcaption class="fslb-caption">First photo &amp; more</figcaption>`,
		`data-caption="First photo &amp; more"`,
		`<div class="swiper-pagination fslb-pagination"></div>`,
	} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("missing %s in\n%s", want, out.HTML)
		}
	}
	if strings.Contains(out.HTML, "doc.pdf") || strings.Contains(out.HTML, `data-fslb-index="2"`) || strings.Contains(out.HTML, "<b>") {
		t.Errorf("pdf, unknown image or caption markup: %s", out.HTML)
	}
	if out := render(t, map[string]string{"id": "791", "captions": "no"}); strings.Contains(out.HTML, "figcaption") ||
		!strings.Contains(out.HTML, `data-captions="0"`) {
		t.Error("captions=no")
	}
	for _, id := range []string{"", "0", "-1", "abc", "5"} {
		if out := render(t, map[string]string{"id": id}); out.HTML != "" || len(out.Assets) != 0 {
			t.Errorf("id %q: %q", id, out.HTML)
		}
	}
	// Titles and captions cannot break out of attributes.
	h.Media[4] = img(4, `"><script>x</script>`)
	h.Media[4] = func(m gp.Media) gp.Media { m.Alt = `" onerror="x`; return m }(h.Media[4])
	h.Put("galleries", "5", Gallery{ID: 5, Title: "x", Images: []int64{4}, Captions: true})
	if out := render(t, map[string]string{"id": "5"}); strings.Contains(out.HTML, `<script`) || strings.Contains(out.HTML, `onerror="`) {
		t.Errorf("escaping: %s", out.HTML)
	}
}

func TestParseBool(t *testing.T) {
	for s, want := range map[string]bool{"1": true, "true": true, "ON": true, "yes": true, "0": false, "no": false, "x": false} {
		if parseBool(s) != want {
			t.Errorf("%q", s)
		}
	}
}

func call(t *testing.T, page, method string, u gp.User, params any) (json.RawMessage, error) {
	t.Helper()
	p, _ := json.Marshal(params)
	var out json.RawMessage
	err := gp.CallExport("admin_api", gp.AdminRequest{Page: page, Method: method, User: u, Params: p}, &out)
	return out, err
}

func TestAPI(t *testing.T) {
	h := newHost(t)
	author := gp.User{ID: 7, Login: "au", Role: "author"}
	other := gp.User{ID: 8, Login: "ot", Role: "author"}
	editor := gp.User{ID: 9, Login: "ed", Role: "editor"}
	h.Put("galleries", "791", Gallery{ID: 791, Title: "Imported", Images: []int64{1}})

	out, err := call(t, "galleries", "save", author, galleryInput{Title: "  My   trip ", Images: []int64{2, 1, 2, 3, 999}, Captions: true})
	if err != nil {
		t.Fatal(err)
	}
	var g galleryInfo
	json.Unmarshal(out, &g)
	if g.ID != 792 || g.Title != "My trip" || len(g.Images) != 2 || g.Images[0] != 2 || g.Owner != 7 {
		t.Errorf("saved: %+v", g)
	}
	if h.Invalidated == 0 {
		t.Error("cache not cleared")
	}
	for _, bad := range []galleryInput{{Title: ""}, {Title: strings.Repeat("x", 201)}, {Title: "x", Images: []int64{-1}},
		{Title: "x", Images: make([]int64, 501)}} {
		if _, err := call(t, "galleries", "save", author, bad); err == nil {
			t.Errorf("accepted %+v", bad.Title)
		}
	}
	// Rights: own galleries, editors all; the dialog page only reads.
	if _, err := call(t, "galleries", "save", other, galleryInput{ID: 792, Title: "Mine now"}); err == nil {
		t.Error("another author changed the gallery")
	}
	if _, err := call(t, "galleries", "delete", author, map[string]int64{"id": 791}); err == nil {
		t.Error("an author deleted an imported gallery")
	}
	if _, err := call(t, "pick", "delete", editor, map[string]int64{"id": 792}); err == nil {
		t.Error("deleted from the dialog page")
	}
	out, _ = call(t, "pick", "list", other, nil)
	var list []galleryInfo
	json.Unmarshal(out, &list)
	if len(list) != 2 || list[0].Title != "Imported" || list[0].Editable || list[1].Editable {
		t.Errorf("list for another author: %+v", list)
	}
	if _, err := call(t, "galleries", "delete", editor, map[string]int64{"id": 792}); err != nil {
		t.Errorf("editor delete: %v", err)
	}
	// Deleted ids are not used again.
	out, _ = call(t, "galleries", "save", editor, galleryInput{Title: "New"})
	json.Unmarshal(out, &g)
	if g.ID != 793 {
		t.Errorf("id after delete: %d", g.ID)
	}
	out, _ = call(t, "galleries", "get", author, map[string]int64{"id": 791})
	if !strings.Contains(string(out), `"thumbs":[{"id":1,"url":"https://example.org/wp-content/uploads/2026/07/p1-150x150.png"`) {
		t.Errorf("get: %s", out)
	}
}

func TestMigrate(t *testing.T) {
	h := newHost(t)
	h.SetStash("post:fslb_gallery", "791", map[string]any{"id": 791, "status": "publish", "title": "Ausflug &amp; mehr",
		"date_gmt": "2026-07-19 10:00:00", "meta": map[string]any{"_fslb_image_ids": []any{776.0, 777.0, 776.0}, "_fslb_show_captions": "0"}})
	h.SetStash("post:fslb_gallery", "800", map[string]any{"id": 800, "status": "draft", "title": "",
		"meta": map[string]any{"_fslb_image_ids": map[string]any{"2": 5.0, "0": "3"}}})
	h.SetStash("post:fslb_gallery", "801", map[string]any{"id": 801, "status": "trash", "title": "Gone"})
	var out struct{ Notice string }
	if err := gp.CallExport("migrate_wordpress", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	var g Gallery
	if !h.Get("galleries", "791", &g) || g.Title != "Ausflug & mehr" || len(g.Images) != 2 || g.Captions || g.Created != "2026-07-19T10:00:00Z" {
		t.Errorf("791: %+v", g)
	}
	if !h.Get("galleries", "800", &g) || g.Title != "Gallery 800" || len(g.Images) != 2 || g.Images[0] != 3 || !g.Captions {
		t.Errorf("800: %+v", g)
	}
	if h.Get("galleries", "801", &g) {
		t.Error("trashed gallery imported")
	}
	if out.Notice != "2 galleries imported." || len(h.Progress) == 0 {
		t.Errorf("notice %q, progress %v", out.Notice, h.Progress)
	}
}
