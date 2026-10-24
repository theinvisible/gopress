package content

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// TermInput is a category or tag as submitted by the terms screens.
type TermInput struct {
	ID          int64 // 0 = new
	Taxonomy    string
	Name        string
	Slug        string // empty: derived from the name
	Description string
	ParentID    int64 // categories only
}

// DefaultCategory returns the id of the default category (0 if unset).
func (s *Service) DefaultCategory(ctx context.Context) int64 {
	var v any
	if ok, err := db.GetOption(ctx, s.db.R, "default_category", &v); err != nil || !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// SaveTerm creates or updates a category or tag (wp_insert_term /
// wp_update_term). A changed slug keeps the old archive URL working.
func (s *Service) SaveTerm(ctx context.Context, u *db.User, r *http.Request, in TermInput) (*db.Term, error) {
	if !auth.Can(u, auth.CapManageCategories) {
		return nil, ErrForbidden
	}
	if in.Taxonomy != db.TaxCategory && in.Taxonomy != db.TaxTag {
		return nil, ErrInvalid
	}
	in.Name = strings.TrimSpace(render.SanitizeText(in.Name))
	if in.Name == "" || len(in.Name) > 200 {
		return nil, &ValidationError{"Please enter a name (up to 200 characters)."}
	}
	in.Description = strings.TrimSpace(in.Description)
	if len(in.Description) > 10000 {
		return nil, &ValidationError{"The description is too long."}
	}
	if !auth.Can(u, auth.CapUnfilteredHTML) {
		in.Description = render.SanitizeComment(in.Description)
	}
	if in.Taxonomy == db.TaxTag {
		in.ParentID = 0
	}
	var old *db.Term
	if in.ID != 0 {
		t, err := db.GetTerm(ctx, s.db.R, in.ID)
		if err != nil {
			return nil, err
		}
		if t == nil || t.Taxonomy != in.Taxonomy {
			return nil, ErrNotFound
		}
		old = t
	}
	if err := s.checkTermParent(ctx, in); err != nil {
		return nil, err
	}
	// Names are unique below the same parent (and among tags).
	siblings, err := db.Terms(ctx, s.db.R, in.Taxonomy)
	if err != nil {
		return nil, err
	}
	for _, t := range siblings {
		if t.ID != in.ID && t.ParentID == in.ParentID && strings.EqualFold(t.Name, in.Name) {
			return nil, &ValidationError{"A term with this name already exists" + map[bool]string{true: " with this parent.", false: "."}[in.Taxonomy == db.TaxCategory]}
		}
	}
	slug, err := s.termSlug(ctx, in)
	if err != nil {
		return nil, err
	}
	t := &db.Term{ID: in.ID, Taxonomy: in.Taxonomy, Slug: slug, Name: in.Name, Description: in.Description, ParentID: in.ParentID}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if old == nil {
			return db.InsertTerm(ctx, tx, t)
		}
		t.Count = old.Count
		if err := db.UpdateTerm(ctx, tx, t); err != nil {
			return err
		}
		if old.Slug != t.Slug {
			return db.AddTermSlugRedirect(ctx, tx, t.Taxonomy, old.Slug, t.Slug, t.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	action := "term_created"
	if old != nil {
		action = "term_updated"
	}
	s.audit(ctx, r, u.ID, action, "term", t.ID, map[string]any{"taxonomy": t.Taxonomy, "name": t.Name})
	s.site.Invalidate()
	return t, nil
}

// termSlug returns the slug for in: an explicit slug must be free, a
// derived one gets -2, -3, … like WordPress.
func (s *Service) termSlug(ctx context.Context, in TermInput) (string, error) {
	explicit := strings.TrimSpace(in.Slug) != ""
	base := SanitizeTitle(in.Slug)
	if !explicit {
		base = SanitizeTitle(in.Name)
	}
	if base == "" {
		return "", &ValidationError{"Please enter a valid slug."}
	}
	if len(base) > 200 {
		base = strings.TrimRight(base[:200], "-")
	}
	slug := base
	for n := 2; ; n++ {
		taken, err := db.TermSlugTaken(ctx, s.db.R, in.Taxonomy, slug, in.ID)
		if err != nil {
			return "", err
		}
		if !taken {
			return slug, nil
		}
		if explicit {
			return "", &ValidationError{"The slug “" + slug + "” is already in use by another term."}
		}
		slug = base + "-" + strconv.Itoa(n)
	}
}

func (s *Service) checkTermParent(ctx context.Context, in TermInput) error {
	for id, depth := in.ParentID, 0; id != 0; depth++ {
		if id == in.ID && in.ID != 0 || depth > 100 {
			return &ValidationError{"A category cannot be its own parent or the child of one of its subcategories."}
		}
		t, err := db.GetTerm(ctx, s.db.R, id)
		if err != nil {
			return err
		}
		if t == nil || t.Taxonomy != in.Taxonomy {
			return &ValidationError{"The parent category does not exist."}
		}
		id = t.ParentID
	}
	return nil
}

// ErrDefaultCategory is returned when deleting the default category.
var ErrDefaultCategory = &ValidationError{"The default category cannot be deleted. Choose another default category first (Settings › Writing)."}

// DeleteTerms deletes categories or tags. Posts that lose their only
// category get the default category; subcategories move up.
func (s *Service) DeleteTerms(ctx context.Context, u *db.User, r *http.Request, taxonomy string, ids []int64) (int, error) {
	if !auth.Can(u, auth.CapManageCategories) {
		return 0, ErrForbidden
	}
	def := s.DefaultCategory(ctx)
	n := 0
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		n = 0
		for _, id := range ids {
			t, err := db.GetTerm(ctx, tx, id)
			if err != nil {
				return err
			}
			if t == nil || t.Taxonomy != taxonomy {
				continue
			}
			if taxonomy == db.TaxCategory && t.ID == def {
				return ErrDefaultCategory
			}
			if err := db.DeleteTerm(ctx, tx, t, def); err != nil {
				return err
			}
			n++
		}
		return db.RecountTerms(ctx, tx)
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.audit(ctx, r, u.ID, "terms_deleted", "term", 0, map[string]any{"taxonomy": taxonomy, "ids": ids})
		s.site.Invalidate()
	}
	return n, nil
}

// SetDefaultCategory makes a category the default for new posts.
func (s *Service) SetDefaultCategory(ctx context.Context, u *db.User, r *http.Request, id int64) error {
	if !auth.Can(u, auth.CapManageOptions) {
		return ErrForbidden
	}
	t, err := db.GetTerm(ctx, s.db.R, id)
	if err != nil {
		return err
	}
	if t == nil || t.Taxonomy != db.TaxCategory {
		return ErrNotFound
	}
	if err := db.SetOption(ctx, s.db.W, "default_category", strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	s.audit(ctx, r, u.ID, "default_category_changed", "term", id, nil)
	s.site.Invalidate()
	return nil
}
