package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func testPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rand.Read(img.Pix) // incompressible
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func (e *testEnv) upload(cur *auth.Current, name string, data []byte, parent int64, csrf bool) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if parent != 0 {
		mw.WriteField("post", strconv.FormatInt(parent, 10))
	}
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	r := httptest.NewRequest("POST", home+"/admin/api/media", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", home)
	if csrf {
		r.Header.Set("X-CSRF-Token", cur.CSRF)
	}
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cur.Session.UserAgent})
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func TestMediaUpload(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	author := e.session(auth.RoleAuthor, db.SessionFull)
	if rec := e.upload(e.session(auth.RoleContributor, db.SessionFull), "a.png", testPNG(20, 20), 0, true); rec.Code != http.StatusForbidden {
		t.Errorf("contributor upload: %d", rec.Code)
	}
	if rec := e.upload(author, "a.png", testPNG(20, 20), 0, false); rec.Code != http.StatusForbidden {
		t.Errorf("upload without CSRF token: %d", rec.Code)
	}
	if rec := e.upload(author, "a.png", testPNG(20, 20), 10, true); rec.Code != http.StatusBadRequest {
		t.Errorf("attach to someone else's post: %d", rec.Code)
	}
	if rec := e.upload(author, "evil.html", []byte("<script>alert(1)</script>"), 0, true); rec.Code != http.StatusBadRequest {
		t.Errorf("HTML upload: %d", rec.Code)
	}
	rec := e.upload(author, `<img src=x onerror=alert(1)>.png`, testPNG(400, 300), 30, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var it mediaItem
	json.Unmarshal(rec.Body.Bytes(), &it)
	if strings.ContainsAny(it.Filename+it.Title, "<>\"") || it.Sizes["thumbnail"].Width != 150 || it.Sizes["medium"].Height != 225 || !it.CanEdit {
		t.Errorf("item = %+v", it)
	}
	p, _ := db.GetPost(ctx, e.d.R, it.ID)
	if p.ParentID != 30 || p.Type != db.TypeAttachment || p.AuthorID != e.users[auth.RoleAuthor].ID {
		t.Errorf("attachment = %+v", p)
	}
	// Served right away (the upload allowlist was rebuilt).
	u, _ := url.Parse(it.URL)
	if rec := e.do("GET", u.Path, nil, nil, nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("GET %s: %d %s", u.Path, rec.Code, rec.Header().Get("Content-Type"))
	}
	// Edit fields; HTML in the alt text is dropped.
	form := url.Values{"alt": {`A "chart" <script>x</script>`}, "title": {"Chart"}, "_csrf": {author.CSRF}}
	rec = e.do("POST", "/admin/api/media/"+strconv.FormatInt(it.ID, 10), author, form, map[string]string{"Origin": home})
	json.Unmarshal(rec.Body.Bytes(), &it)
	if rec.Code != 200 || it.Title != "Chart" || strings.Contains(it.Alt, "<") {
		t.Errorf("update: %d %+v", rec.Code, it)
	}
	// The editor may delete it; the files go away.
	ed := e.session(auth.RoleEditor, db.SessionFull)
	files, _ := filepath.Glob(filepath.Join(e.uploads, "*", "*", "*"))
	if len(files) < 3 {
		t.Fatalf("files = %v", files)
	}
	rec = e.do("POST", "/admin/api/media/"+strconv.FormatInt(it.ID, 10)+"/delete", ed, url.Values{"_csrf": {ed.CSRF}}, map[string]string{"Origin": home})
	if rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s survived the deletion", f)
		}
	}
	if rec := e.do("GET", u.Path, nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleted file served: %d", rec.Code)
	}
	// The maximum upload size applies.
	db.SetOption(ctx, e.d.W, "upload_max_size", "2000")
	e.pub.Invalidate()
	if rec := e.upload(author, "big.png", testPNG(400, 400), 0, true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", rec.Code)
	}
	if left, _ := filepath.Glob(filepath.Join(e.uploads, ".tmp", "*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}
