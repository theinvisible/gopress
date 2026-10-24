package admin

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
)

// taxonomy describes a terms screen (edit-tags.php).
type taxonomy struct {
	Tax, Base, Singular, Plural, Screen, Filter string
	Hierarchical                                bool
}

var (
	taxCategory = taxonomy{Tax: db.TaxCategory, Base: "/admin/categories/", Singular: "Category", Plural: "Categories",
		Screen: "posts-categories", Filter: "cat", Hierarchical: true}
	taxTag = taxonomy{Tax: db.TaxTag, Base: "/admin/tags/", Singular: "Tag", Plural: "Tags", Screen: "posts-tags", Filter: "tag"}
)

func (a *Admin) registerTerms(mux *http.ServeMux) {
	for _, tx := range []taxonomy{taxCategory, taxTag} {
		tx := tx
		with := func(h func(http.ResponseWriter, *http.Request, *reqCtx, taxonomy) error) handler {
			return func(w http.ResponseWriter, r *http.Request, c *reqCtx) error { return h(w, r, c, tx) }
		}
		mux.Handle("GET "+tx.Base+"{$}", a.page(auth.CapManageCategories, with(a.termList)))
		mux.Handle("POST "+tx.Base+"{$}", a.page(auth.CapManageCategories, with(a.termAdd)))
		mux.Handle("POST "+tx.Base+"bulk", a.page(auth.CapManageCategories, with(a.termBulk)))
		mux.Handle("GET "+tx.Base+"{id}/edit", a.page(auth.CapManageCategories, with(a.termEditPage)))
		mux.Handle("POST "+tx.Base+"{id}/edit", a.page(auth.CapManageCategories, with(a.termSave)))
		mux.Handle("POST "+tx.Base+"{id}/delete", a.page(auth.CapManageCategories, with(a.termDelete)))
	}
	mux.Handle("POST /admin/categories/{id}/default", a.page(auth.CapManageOptions, a.termMakeDefault))
}

type termListData struct {
	basePage
	Tx       taxonomy
	Rows     []termRow
	Search   string
	Total    int
	Paged    int
	Pages    int
	PageURL  func(int) string
	SortURL  func(string) string
	Parents  []option
	Form     content.TermInput
	Error    string
	Default  int64
	CanDflt  bool
}

type termRow struct {
	db.Term
	Level     int
	PostsURL  string
	ViewURL   string
	IsDefault bool
}

var termMessages = map[string]notice{
	"added":   {"success", "Item added."},
	"updated": {"success", "Item updated."},
	"deleted": {"success", "Items deleted."},
	"default": {"success", "Default category changed."},
}

const termsPerPage = 30

// GET /admin/categories/ and /admin/tags/
func (a *Admin) termList(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy) error {
	return a.renderTermList(w, r, c, tx, content.TermInput{}, "")
}

func (a *Admin) renderTermList(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy, form content.TermInput, errMsg string) error {
	ctx := r.Context()
	q := r.URL.Query()
	d := &termListData{basePage: a.base(ctx, c, tx.Plural, tx.Screen), Tx: tx, Form: form, Error: errMsg,
		Search: strings.TrimSpace(q.Get("s")), Default: a.content.DefaultCategory(ctx), CanDflt: auth.Can(c.user, auth.CapManageOptions)}
	d.Scripts = []string{"list.js", "terms.js"}
	if msg, ok := termMessages[q.Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	orderBy, order := q.Get("orderby"), q.Get("order")
	terms, err := db.AdminTerms(ctx, a.db.R, db.AdminTermQuery{Taxonomy: tx.Tax, Search: d.Search, OrderBy: orderBy, Desc: order == "desc"})
	if err != nil {
		return err
	}
	// Categories in tree order unless searching or sorting.
	levels := map[int64]int{}
	if tx.Hierarchical && d.Search == "" && orderBy == "" {
		terms, levels = termTree(terms)
	}
	all, err := db.Terms(ctx, a.db.R, tx.Tax)
	if err != nil {
		return err
	}
	if tx.Hierarchical {
		tree, lv := termTree(all)
		for _, t := range tree {
			d.Parents = append(d.Parents, option{Value: strconv.FormatInt(t.ID, 10), Label: t.Name, Level: lv[t.ID],
				Selected: t.ID == form.ParentID})
		}
	}
	d.Total = len(terms)
	d.Pages = (d.Total + termsPerPage - 1) / termsPerPage
	d.Paged = min(max(1, int(atoi64(q.Get("paged")))), max(d.Pages, 1))
	from := (d.Paged - 1) * termsPerPage
	for _, t := range terms[from:min(from+termsPerPage, len(terms))] {
		d.Rows = append(d.Rows, termRow{Term: t, Level: levels[t.ID], IsDefault: tx.Hierarchical && t.ID == d.Default,
			PostsURL: "/admin/posts/?" + tx.Filter + "=" + strconv.FormatInt(t.ID, 10), ViewURL: c.links.TermURL(&t)})
	}
	d.PageURL = func(n int) string { return withQuery(r.URL.RequestURI(), "paged", n) }
	d.SortURL = func(col string) string {
		o := "asc"
		if orderBy == col && order != "desc" {
			o = "desc"
		}
		return withQuery(r.URL.RequestURI(), "orderby", col, "order", o, "paged", "")
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "terms", d)
}

// termTree orders terms parent-first (children by name) and returns the
// nesting level of each.
func termTree(terms []db.Term) ([]db.Term, map[int64]int) {
	children := map[int64][]db.Term{}
	ids := map[int64]bool{}
	for _, t := range terms {
		ids[t.ID] = true
	}
	for _, t := range terms {
		p := t.ParentID
		if !ids[p] {
			p = 0 // orphans at the top
		}
		children[p] = append(children[p], t)
	}
	var out []db.Term
	levels := map[int64]int{}
	var walk func(parent int64, level int)
	walk = func(parent int64, level int) {
		list := children[parent]
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		for _, t := range list {
			if _, seen := levels[t.ID]; seen {
				continue
			}
			levels[t.ID] = level
			out = append(out, t)
			walk(t.ID, level+1)
		}
	}
	walk(0, 0)
	return out, levels
}

func readTermForm(r *http.Request, tx taxonomy, id int64) content.TermInput {
	in := content.TermInput{ID: id, Taxonomy: tx.Tax, Name: r.PostFormValue("tag-name"), Slug: r.PostFormValue("slug"),
		Description: r.PostFormValue("description")}
	if tx.Hierarchical {
		in.ParentID = atoi64(r.PostFormValue("parent"))
	}
	return in
}

// termError turns validation errors into a message for the form.
func termError(err error) (string, bool) {
	var ve *content.ValidationError
	if errors.As(err, &ve) {
		return ve.Msg, true
	}
	return "", false
}

// POST /admin/categories/ (add)
func (a *Admin) termAdd(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy) error {
	in := readTermForm(r, tx, 0)
	if _, err := a.content.SaveTerm(r.Context(), c.user, r, in); err != nil {
		if msg, ok := termError(err); ok {
			return a.renderTermList(w, r, c, tx, in, msg)
		}
		return err
	}
	return seeOther(w, r, tx.Base+"?msg=added")
}

// POST /admin/categories/bulk (delete)
func (a *Admin) termBulk(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy) error {
	if r.PostFormValue("action") != "delete" {
		return seeOther(w, r, tx.Base)
	}
	return a.deleteTerms(w, r, c, tx, parseIDs(r.PostForm["delete_tags[]"]))
}

// POST /admin/categories/{id}/delete
func (a *Admin) termDelete(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy) error {
	return a.deleteTerms(w, r, c, tx, []int64{atoi64(r.PathValue("id"))})
}

func (a *Admin) deleteTerms(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy, ids []int64) error {
	if _, err := a.content.DeleteTerms(r.Context(), c.user, r, tx.Tax, ids); err != nil {
		if msg, ok := termError(err); ok {
			return a.renderTermList(w, r, c, tx, content.TermInput{}, msg)
		}
		return err
	}
	return seeOther(w, r, tx.Base+"?msg=deleted")
}

type termEditData struct {
	basePage
	Tx      taxonomy
	Term    *db.Term
	Form    content.TermInput
	Parents []option
	Error   string
	ViewURL string
}

func (a *Admin) loadTerm(r *http.Request, tx taxonomy) (*db.Term, error) {
	t, err := db.GetTerm(r.Context(), a.db.R, atoi64(r.PathValue("id")))
	if err != nil {
		return nil, err
	}
	if t == nil || t.Taxonomy != tx.Tax {
		return nil, errNotFound
	}
	return t, nil
}

// GET /admin/categories/{id}/edit
func (a *Admin) termEditPage(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy) error {
	t, err := a.loadTerm(r, tx)
	if err != nil {
		return err
	}
	return a.renderTermEdit(w, r, c, tx, t, content.TermInput{ID: t.ID, Taxonomy: t.Taxonomy, Name: t.Name, Slug: t.Slug,
		Description: t.Description, ParentID: t.ParentID}, "")
}

func (a *Admin) renderTermEdit(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy, t *db.Term, form content.TermInput, errMsg string) error {
	ctx := r.Context()
	d := &termEditData{basePage: a.base(ctx, c, "Edit "+tx.Singular, tx.Screen), Tx: tx, Term: t, Form: form, Error: errMsg,
		ViewURL: c.links.TermURL(t)}
	if tx.Hierarchical {
		all, err := db.Terms(ctx, a.db.R, tx.Tax)
		if err != nil {
			return err
		}
		tree, lv := termTree(all)
		// Neither the term itself nor its descendants can be its parent.
		skip := map[int64]bool{t.ID: true}
		for _, x := range tree {
			if skip[x.ParentID] {
				skip[x.ID] = true
			}
		}
		for _, x := range tree {
			if !skip[x.ID] {
				d.Parents = append(d.Parents, option{Value: strconv.FormatInt(x.ID, 10), Label: x.Name, Level: lv[x.ID], Selected: x.ID == form.ParentID})
			}
		}
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "term-edit", d)
}

// POST /admin/categories/{id}/edit (form or quick edit with JSON answer)
func (a *Admin) termSave(w http.ResponseWriter, r *http.Request, c *reqCtx, tx taxonomy) error {
	t, err := a.loadTerm(r, tx)
	if err != nil {
		return err
	}
	in := readTermForm(r, tx, t.ID)
	if r.PostFormValue("quick") != "" {
		in.Description, in.ParentID = t.Description, t.ParentID
	}
	saved, err := a.content.SaveTerm(r.Context(), c.user, r, in)
	if err != nil {
		msg, ok := termError(err)
		if !ok {
			return err
		}
		if isAPI(r) {
			return badRequest(msg)
		}
		return a.renderTermEdit(w, r, c, tx, t, in, msg)
	}
	if isAPI(r) {
		writeJSON(w, http.StatusOK, map[string]string{"name": saved.Name, "slug": saved.Slug})
		return nil
	}
	return seeOther(w, r, tx.Base+"?msg=updated")
}

// POST /admin/categories/{id}/default
func (a *Admin) termMakeDefault(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if err := a.content.SetDefaultCategory(r.Context(), c.user, r, atoi64(r.PathValue("id"))); err != nil {
		if errors.Is(err, content.ErrNotFound) {
			return errNotFound
		}
		return err
	}
	return seeOther(w, r, taxCategory.Base+"?msg=default")
}
