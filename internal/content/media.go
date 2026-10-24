package content

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/media"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/site"
)

// SetMediaStore enables uploads.
func (s *Service) SetMediaStore(m *media.Store) { s.media = m }

// MediaStore returns the upload store (nil if uploads are disabled).
func (s *Service) MediaStore() *media.Store { return s.media }

// ImageSizes returns the configured image sizes.
func ImageSizes(st *site.Settings) []media.Size {
	out := make([]media.Size, 0, len(st.ImageSizes))
	for _, sz := range st.ImageSizes {
		out = append(out, media.Size{Name: sz.Name, Width: sz.Width, Height: sz.Height, Crop: sz.Crop})
	}
	return out
}

// Upload stores the temp file tmp (from MediaStore().CreateTemp) as a new
// attachment of parentID (0 = unattached).
func (e *Editor) Upload(ctx context.Context, tmp, name string, parentID int64) (*db.Post, *db.Media, error) {
	s := e.s
	if s.media == nil {
		return nil, nil, errors.New("uploads are not configured")
	}
	if !auth.Can(e.user, auth.CapUploadFiles) {
		s.media.RemoveTemp(tmp)
		return nil, nil, ErrForbidden
	}
	if parentID != 0 {
		parent, err := db.GetPost(ctx, s.db.R, parentID)
		if err != nil || parent.Type == db.TypeAttachment || !auth.CanEditPost(e.user, parent) {
			s.media.RemoveTemp(tmp)
			return nil, nil, &ValidationError{"You cannot attach files to this post."}
		}
	}
	st, err := s.site.Settings(ctx)
	if err != nil {
		s.media.RemoveTemp(tmp)
		return nil, nil, err
	}
	res, err := s.media.Save(ctx, media.Upload{Temp: tmp, Name: name, Sizes: ImageSizes(st), YearMonth: st.UploadsYearMonth})
	if err != nil {
		if errors.Is(err, media.ErrType) || errors.Is(err, media.ErrMismatch) || errors.Is(err, media.ErrTooLarge) || errors.Is(err, media.ErrCorrupt) {
			return nil, nil, &ValidationError{err.Error()}
		}
		return nil, nil, err
	}
	now := s.now()
	title := strings.TrimSpace(render.SanitizeText(strings.ReplaceAll(res.Title, "-", " ")))
	if title == "" {
		title = res.Title
	}
	p := &db.Post{Type: db.TypeAttachment, Status: db.StatusInherit, ParentID: parentID, AuthorID: e.user.ID, Title: title,
		Date: db.Time{Time: wall(now.In(st.Loc))}, DateGMT: db.Time{Time: now.UTC().Truncate(time.Second)},
		Modified: db.Time{Time: wall(now.In(st.Loc))}, ModifiedGMT: db.Time{Time: now.UTC().Truncate(time.Second)},
		CommentStatus: "closed", PingStatus: "closed", MimeType: res.Mime, ContentFormat: "html",
		GUID: st.Home + "/wp-content/uploads/" + res.File}
	m := &db.Media{File: res.File, Mime: res.Mime, Width: res.Width, Height: res.Height, Filesize: res.Filesize,
		Sizes: res.Sizes, Original: res.Original}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		slug, err := UniqueSlug(ctx, tx, SanitizeTitle(title), p)
		if err != nil {
			return err
		}
		p.Slug = slug
		if err := db.InsertPost(ctx, tx, p); err != nil {
			return err
		}
		m.PostID = p.ID
		return db.InsertMedia(ctx, tx, m)
	})
	if err != nil {
		s.media.Delete(m, res.Original)
		return nil, nil, err
	}
	s.audit(ctx, e.r, e.user.ID, "media_uploaded", "attachment", p.ID, map[string]any{"file": res.File, "size": res.Filesize})
	s.site.Invalidate() // the upload allowlist
	return p, m, nil
}

// AttachmentInput are the editable fields of an attachment.
type AttachmentInput struct {
	Title, Alt, Caption, Description *string // nil = unchanged
}

// UpdateAttachment changes title, alternative text, caption (excerpt) and
// description (content) of an attachment.
func (e *Editor) UpdateAttachment(ctx context.Context, id int64, in AttachmentInput) (*db.Post, *db.Media, error) {
	s := e.s
	p, err := db.GetPost(ctx, s.db.R, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && p.Type != db.TypeAttachment) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if !auth.CanEditPost(e.user, p) {
		return nil, nil, ErrForbidden
	}
	m, err := db.GetMedia(ctx, s.db.R, id)
	if err != nil {
		return nil, nil, err
	}
	clean := func(v *string, max int) (string, error) {
		t := strings.TrimSpace(*v)
		if len(t) > max {
			return "", &ValidationError{"A field is too long."}
		}
		return t, nil
	}
	if in.Title != nil {
		if p.Title, err = clean(in.Title, 500); err != nil {
			return nil, nil, err
		}
		p.Title = render.SanitizeText(p.Title)
	}
	if in.Caption != nil {
		if p.Excerpt, err = clean(in.Caption, 5000); err != nil {
			return nil, nil, err
		}
	}
	if in.Description != nil {
		if p.Content, err = clean(in.Description, 50000); err != nil {
			return nil, nil, err
		}
	}
	if !auth.Can(e.user, auth.CapUnfilteredHTML) {
		p.Excerpt = render.SanitizeComment(p.Excerpt)
		p.Content = render.SanitizePost(p.Content)
	}
	alt := ""
	if m != nil {
		alt = m.Alt
	}
	if in.Alt != nil {
		if alt, err = clean(in.Alt, 1000); err != nil {
			return nil, nil, err
		}
		alt = render.SanitizeText(alt)
	}
	st, err := s.site.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	p.Modified, p.ModifiedGMT = db.Time{Time: wall(now.In(st.Loc))}, db.Time{Time: now.UTC().Truncate(time.Second)}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.UpdatePost(ctx, tx, p); err != nil {
			return err
		}
		if m != nil && alt != m.Alt {
			m.Alt = alt
			return db.SetMediaAlt(ctx, tx, id, alt)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	s.audit(ctx, e.r, e.user.ID, "media_updated", "attachment", id, nil)
	s.site.Invalidate()
	return p, m, nil
}

// DeleteAttachment deletes an attachment with its files. Posts that use
// it as featured image lose it; images in post content stay as they are.
func (e *Editor) DeleteAttachment(ctx context.Context, id int64) error {
	s := e.s
	p, err := db.GetPost(ctx, s.db.R, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && p.Type != db.TypeAttachment) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !auth.CanDeletePost(e.user, p) {
		return ErrForbidden
	}
	m, err := db.GetMedia(ctx, s.db.R, id)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE posts SET featured_media_id = 0 WHERE featured_media_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM post_meta WHERE key = '_thumbnail_id' AND value = ?`, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		return db.DeletePost(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	if m != nil && s.media != nil {
		if err := s.media.Delete(m, m.Original); err != nil {
			s.log.Warn("delete media files", "id", id, "err", err)
		}
	}
	s.audit(ctx, e.r, e.user.ID, "media_deleted", "attachment", id, map[string]any{"title": p.Title})
	s.site.Invalidate()
	return nil
}

// RegenerateSizes creates the image sizes of all images again (after the
// size settings changed). missing limits it to images without all sizes.
func (s *Service) RegenerateSizes(ctx context.Context, missing bool, progress func(file string, err error)) error {
	if s.media == nil {
		return errors.New("uploads are not configured")
	}
	st, err := s.site.Settings(ctx)
	if err != nil {
		return err
	}
	sizes := ImageSizes(st)
	ids, err := db.AllMediaIDs(ctx, s.db.R)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m, err := db.GetMedia(ctx, s.db.R, id)
		if err != nil || m == nil || !strings.HasPrefix(m.Mime, "image/") || m.Mime == "image/gif" {
			continue
		}
		if missing && hasAllSizes(m, sizes) {
			continue
		}
		newSizes, err := s.media.Regenerate(ctx, m, m.Original, sizes)
		if err == nil {
			err = db.SetMediaSizes(ctx, s.db.W, id, newSizes)
		}
		if progress != nil {
			progress(m.File, err)
		}
	}
	s.site.Invalidate()
	return nil
}

// hasAllSizes reports whether every size that the image is large enough
// for exists.
func hasAllSizes(m *db.Media, sizes []media.Size) bool {
	for _, sz := range sizes {
		if _, ok := m.Sizes[sz.Name]; ok {
			continue
		}
		if (sz.Width > 0 && m.Width > sz.Width) || (sz.Height > 0 && m.Height > sz.Height) {
			return false
		}
	}
	return true
}
