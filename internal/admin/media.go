package admin

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/media"
)

func (a *Admin) registerMedia(mux *http.ServeMux) {
	mux.Handle("GET /admin/media/{$}", a.page(auth.CapUploadFiles, a.mediaLibrary))
	mux.Handle("GET /admin/api/media", a.page(auth.CapUploadFiles, a.apiMedia))
	mux.Handle("POST /admin/api/media", a.page(auth.CapUploadFiles, a.apiUpload))
	mux.Handle("GET /admin/api/media/{id}", a.page(auth.CapUploadFiles, a.apiMediaGet))
	mux.Handle("POST /admin/api/media/{id}", a.page(auth.CapUploadFiles, a.apiMediaUpdate))
	mux.Handle("POST /admin/api/media/{id}/delete", a.page(auth.CapUploadFiles, a.apiMediaDelete))
}

type mediaItem struct {
	ID          int64             `json:"id"`
	Title       string            `json:"title"`
	Alt         string            `json:"alt"`
	Caption     string            `json:"caption"`
	Description string            `json:"description"`
	Mime        string            `json:"mime"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	Filename    string            `json:"filename"`
	Filesize    int64             `json:"filesize"`
	Date        string            `json:"date"`
	Author      string            `json:"author"`
	Parent      string            `json:"parent,omitempty"`     // title of the post it is attached to
	ParentURL   string            `json:"parent_url,omitempty"` // its edit screen
	URL         string            `json:"url"`
	Thumb       string            `json:"thumb"`
	Page        string            `json:"page"`
	CanEdit     bool              `json:"can_edit"`
	CanDelete   bool              `json:"can_delete"`
	Sizes       map[string]sizeJS `json:"sizes"`
}

type sizeJS struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func (a *Admin) mediaItemOf(r *http.Request, c *reqCtx, p *db.Post, m *db.Media) mediaItem {
	it := mediaItem{ID: p.ID, Title: p.Title, Alt: m.Alt, Caption: p.Excerpt, Description: p.Content, Mime: m.Mime,
		Width: m.Width, Height: m.Height, Filename: path.Base(m.File), Filesize: m.Filesize,
		Date: p.Date.Format("2006/01/02"), URL: c.links.UploadURL(m.File), Page: c.links.PostURL(p),
		CanEdit: auth.CanEditPost(c.user, p), CanDelete: auth.CanDeletePost(c.user, p),
		Sizes: map[string]sizeJS{"full": {c.links.UploadURL(m.File), m.Width, m.Height}}}
	if strings.HasPrefix(m.Mime, "image/") {
		it.Thumb = mediaURL(c, m, "thumbnail")
		if _, ok := m.Sizes["medium"]; ok {
			it.Thumb = mediaURL(c, m, "medium") // sharper in the grid
		}
	}
	for name, s := range m.Sizes {
		it.Sizes[name] = sizeJS{mediaURL(c, m, name), s.Width, s.Height}
	}
	if u, err := db.GetUser(r.Context(), a.db.R, p.AuthorID); err == nil {
		it.Author = u.DisplayName
	}
	if p.ParentID != 0 {
		if parent, err := db.GetPost(r.Context(), a.db.R, p.ParentID); err == nil {
			it.Parent = parent.Title
			if auth.CanEditPost(c.user, parent) {
				it.ParentURL = editURL(parent)
			}
		}
	}
	return it
}

// GET /admin/api/media?s=&page=&images=1&type=&m=&unattached=1
func (a *Admin) apiMedia(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	q := r.URL.Query()
	page := max(1, int(atoi64(q.Get("page"))))
	const n = 40
	posts, media, total, err := db.AdminMedia(r.Context(), a.db.R, db.MediaQuery{Search: q.Get("s"),
		ImagesOnly: q.Get("images") == "1", Type: q.Get("type"), Month: q.Get("m"), Unattached: q.Get("unattached") == "1",
		Limit: n, Offset: (page - 1) * n})
	if err != nil {
		return err
	}
	items := make([]mediaItem, 0, len(posts))
	for i := range posts {
		if m := media[posts[i].ID]; m != nil {
			items = append(items, a.mediaItemOf(r, c, &posts[i], m))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "pages": (total + n - 1) / n})
	return nil
}

// POST /admin/api/media: multipart upload with the fields "post" (parent,
// optional, before the file) and "file". The body streams into a temp
// file; the size limit is Settings › Media › maximum upload size.
func (a *Admin) apiUpload(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	store := a.content.MediaStore()
	if store == nil {
		return badRequest("Uploads are not available.")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("Invalid upload.")
	}
	var parent int64
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return badRequest("No file was uploaded.")
		}
		if err != nil {
			return a.uploadError(err)
		}
		switch part.FormName() {
		case "post":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			parent = atoi64(strings.TrimSpace(string(b)))
			continue
		case "file":
		default:
			continue
		}
		name := part.FileName()
		if name == "" {
			return badRequest("No file was uploaded.")
		}
		f, tmp, err := store.CreateTemp()
		if err != nil {
			return err
		}
		_, err = io.Copy(f, part)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			store.RemoveTemp(tmp)
			return a.uploadError(err)
		}
		p, m, err := a.content.As(c.user, r).Upload(ctx, tmp, name, parent)
		if err != nil {
			return a.contentError(err)
		}
		writeJSON(w, http.StatusCreated, a.mediaItemOf(r, c, p, m))
		return nil
	}
}

func (a *Admin) uploadError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return &httpError{http.StatusRequestEntityTooLarge, "The file is larger than the maximum upload size (Settings › Media)."}
	}
	return badRequest("The upload failed.")
}

// POST /admin/api/media/{id}: title, alt, caption, description (only the
// submitted fields change).
func (a *Admin) apiMediaUpdate(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if err := r.ParseForm(); err != nil {
		return badRequest("Invalid request.")
	}
	var in content.AttachmentInput
	field := func(k string) *string {
		if _, ok := r.PostForm[k]; !ok {
			return nil
		}
		v := r.PostFormValue(k)
		return &v
	}
	in.Title, in.Alt, in.Caption, in.Description = field("title"), field("alt"), field("caption"), field("description")
	p, m, err := a.content.As(c.user, r).UpdateAttachment(r.Context(), atoi64(r.PathValue("id")), in)
	if err != nil {
		return a.contentError(err)
	}
	if m == nil {
		m = &db.Media{}
	}
	writeJSON(w, http.StatusOK, a.mediaItemOf(r, c, p, m))
	return nil
}

// POST /admin/api/media/{id}/delete
func (a *Admin) apiMediaDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if err := a.content.As(c.user, r).DeleteAttachment(r.Context(), atoi64(r.PathValue("id"))); err != nil {
		return a.contentError(err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	return nil
}

type mediaLibraryData struct {
	basePage
	Months  []option
	Accept  string
	MaxSize string
}

// GET /admin/media/
func (a *Admin) mediaLibrary(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	d := &mediaLibraryData{basePage: a.base(ctx, c, "Media Library", "media-library"), Accept: media.Accept(),
		MaxSize: strconv.FormatInt(c.settings.UploadMaxBytes>>20, 10) + " MB"}
	d.Scripts = []string{"upload.js", "media.js"}
	months, err := db.PostMonths(ctx, a.db.R, db.TypeAttachment)
	if err != nil {
		return err
	}
	for _, m := range months {
		if t, err := time.Parse("200601", m); err == nil {
			d.Months = append(d.Months, option{Value: m, Label: t.Format("January 2006")})
		}
	}
	return a.render(w, "media", d)
}

// GET /admin/api/media/{id}: one attachment.
func (a *Admin) apiMediaGet(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	p, err := db.GetPost(r.Context(), a.db.R, atoi64(r.PathValue("id")))
	if err != nil || p.Type != db.TypeAttachment {
		return errNotFound
	}
	m, err := db.GetMedia(r.Context(), a.db.R, p.ID)
	if err != nil || m == nil {
		return errNotFound
	}
	writeJSON(w, http.StatusOK, a.mediaItemOf(r, c, p, m))
	return nil
}
