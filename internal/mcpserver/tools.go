package mcpserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/site"
)

// call is the context of one tool call.
type call struct {
	ctx   context.Context
	user  *db.User // capabilities limited to the token's scopes
	token *db.AccessToken
	ed    *content.Editor
	st    *site.Settings
	links *site.Links
}

func (s *Server) begin(ctx context.Context, req *mcp.CallToolRequest) (*call, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil, errors.New("not authenticated")
	}
	x := req.Extra.TokenInfo.Extra
	u, _ := x[extraUser].(*db.User)
	t, _ := x[extraToken].(*db.AccessToken)
	ip, _ := x[extraIP].(string)
	if u == nil || t == nil {
		return nil, errors.New("not authenticated")
	}
	c := &call{ctx: auth.WithAccessToken(ctx, t, ip), user: u, token: t}
	c.ed = s.opts.Content.As(u, nil)
	var err error
	if c.st, err = s.opts.Site.Settings(ctx); err != nil {
		return nil, s.internal(err)
	}
	if c.links, err = s.opts.Site.Links(ctx); err != nil {
		return nil, s.internal(err)
	}
	if c.st.Loc == nil {
		c.st.Loc = time.UTC
	}
	return c, nil
}

// internal logs err and returns a generic error for the agent.
func (s *Server) internal(err error) error {
	s.log.Error("mcp", "err", err)
	return errors.New("internal error")
}

// userError turns errors of the content service into messages for the
// agent.
func (s *Server) userError(err error) error {
	var ve *content.ValidationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ve):
		return errors.New(ve.Msg)
	case errors.Is(err, content.ErrForbidden):
		return errors.New("not allowed: the access token's scopes or the user's role do not permit this")
	case errors.Is(err, content.ErrInvalid):
		return errors.New("invalid input")
	case errors.Is(err, content.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return errNotFound
	}
	return s.internal(err)
}

var errNotFound = errors.New("not found")

func boolPtr(b bool) *bool { return &b }

var (
	readOnly  = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}
	additive  = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
	modifying = &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)}
)

// addTools registers the tools usable with scopes.
func (s *Server) addTools(srv *mcp.Server, scopes []string) {
	has := func(sc string) bool { return slices.Contains(scopes, sc) }
	mcp.AddTool(srv, &mcp.Tool{Name: "get_site_info", Annotations: readOnly,
		Description: "Site name, tagline and URL, time zone, permalink structure, the user and the permissions of this access token, upload limits."}, s.siteInfo)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_posts", Annotations: readOnly,
		Description: "List posts or pages, newest first: published ones and the user's own drafts, pending, scheduled and private ones."}, s.listPosts)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_post", Annotations: readOnly,
		Description: "Get one post or page with its HTML content, by id, slug or URL."}, s.getPost)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_terms", Annotations: readOnly,
		Description: "List categories or tags with their ids."}, s.listTerms)
	if has(auth.ScopeWrite) {
		mcp.AddTool(srv, &mcp.Tool{Name: "create_post", Annotations: additive,
			Description: "Create a post or page. Content is Markdown by default (or HTML). Status defaults to draft. " +
				"Without the content:publish scope, publish requests become pending (submitted for review). " +
				"Returns the post with edit_url, the admin screen where a person can review it."}, s.createPost)
		mcp.AddTool(srv, &mcp.Tool{Name: "update_post", Annotations: modifying,
			Description: "Change a post or page of the user. Pass expected_modified from get_post; only the fields given are changed. " +
				"Published posts can only be changed with the content:publish scope."}, s.updatePost)
	}
	if has(auth.ScopeTerms) {
		mcp.AddTool(srv, &mcp.Tool{Name: "create_category", Annotations: additive,
			Description: "Create a category. Tags need no tool: create_post and update_post create unknown tags."}, s.createCategory)
	}
	if has(auth.ScopeMedia) {
		mcp.AddTool(srv, &mcp.Tool{Name: "upload_media", Annotations: additive,
			Description: "Upload an image (JPEG, PNG, GIF, WebP) or PDF as base64. Photo metadata (EXIF, GPS) is removed. " +
				"Returns the URL, the generated sizes and HTML to put into post content."}, s.uploadMedia)
		mcp.AddTool(srv, &mcp.Tool{Name: "list_media", Annotations: readOnly,
			Description: "List files in the media library, newest first."}, s.listMedia)
	}
}

// Site information.

type siteInfoIn struct{}

type userOut struct {
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type siteInfoOut struct {
	Name           string   `json:"name"`
	Tagline        string   `json:"tagline"`
	URL            string   `json:"url"`
	TimeZone       string   `json:"time_zone"`
	Now            string   `json:"now" jsonschema:"current site time (YYYY-MM-DDTHH:MM:SS)"`
	Permalinks     string   `json:"permalink_structure"`
	User           userOut  `json:"user"`
	Scopes         []string `json:"scopes"`
	CanPublish     bool     `json:"can_publish"`
	CanEditPages   bool     `json:"can_edit_pages"`
	UploadMaxBytes int64    `json:"upload_max_bytes,omitempty"`
	UploadTypes    []string `json:"upload_types,omitempty"`
}

func (s *Server) siteInfo(ctx context.Context, req *mcp.CallToolRequest, _ siteInfoIn) (*mcp.CallToolResult, siteInfoOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, siteInfoOut{}, err
	}
	out := siteInfoOut{Name: html.UnescapeString(c.st.Name), Tagline: html.UnescapeString(c.st.Description), URL: c.links.Home(),
		TimeZone: c.st.Loc.String(), Now: time.Now().In(c.st.Loc).Format("2006-01-02T15:04:05"), Permalinks: c.st.Permalink,
		User: userOut{c.user.Login, c.user.DisplayName, c.user.Role}, Scopes: auth.TokenScopes(c.token),
		CanPublish: auth.Can(c.user, auth.CapPublishPosts), CanEditPages: auth.Can(c.user, auth.CapEditPages)}
	if auth.Can(c.user, auth.CapUploadFiles) {
		out.UploadMaxBytes = s.uploadLimit(c.st)
		out.UploadTypes = slices.Sorted(func(yield func(string) bool) {
			for ext := range uploadTypes {
				if !yield(ext) {
					return
				}
			}
		})
	}
	return nil, out, nil
}

// Posts.

type termRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type postOut struct {
	ID            int64     `json:"id"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	Title         string    `json:"title"`
	Content       string    `json:"content,omitempty" jsonschema:"HTML"`
	Excerpt       string    `json:"excerpt,omitempty"`
	Slug          string    `json:"slug,omitempty"`
	Date          string    `json:"date,omitempty" jsonschema:"publication date in site time; empty for unscheduled drafts"`
	Modified      string    `json:"modified" jsonschema:"last change (UTC); pass it as expected_modified to update_post"`
	Author        string    `json:"author"`
	Mine          bool      `json:"mine" jsonschema:"written by the user of this token"`
	Categories    []termRef `json:"categories,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	FeaturedMedia int64     `json:"featured_media,omitempty"`
	Parent        int64     `json:"parent,omitempty"`
	CommentStatus string    `json:"comment_status,omitempty"`
	Protected     bool      `json:"password_protected,omitempty"`
	Link          string    `json:"link"`
	EditURL       string    `json:"edit_url,omitempty" jsonschema:"admin screen to review and publish"`
	Note          string    `json:"note,omitempty"`
}

// visible reports whether the user of a token may read p: published posts
// and pages, and everything of their own.
func visible(u *db.User, p *db.Post) bool {
	if p.Type != db.TypePost && p.Type != db.TypePage {
		return false
	}
	return p.AuthorID == u.ID || p.Status == db.StatusPublish
}

func (s *Server) postOut(c *call, p *db.Post, full bool, authors map[int64]string) (postOut, error) {
	o := postOut{ID: p.ID, Type: p.Type, Status: p.Status, Title: html.UnescapeString(p.Title), Slug: p.Slug,
		Modified: p.ModifiedGMT.UTC().Format(time.RFC3339), Mine: p.AuthorID == c.user.ID, Link: c.links.PostURL(p)}
	if !p.DateGMT.IsZero() || p.Status == db.StatusPublish || p.Status == db.StatusFuture {
		o.Date = p.Date.Format("2006-01-02T15:04:05")
	}
	if name, ok := authors[p.AuthorID]; ok {
		o.Author = name
	} else if u, err := db.GetUser(c.ctx, s.opts.DB.R, p.AuthorID); err == nil {
		o.Author = u.DisplayName
		if authors != nil {
			authors[p.AuthorID] = u.DisplayName
		}
	}
	if auth.CanEditPost(c.user, p) {
		o.EditURL = c.links.URL(editPath(p))
	}
	o.Protected = p.Password != ""
	if !full {
		return o, nil
	}
	o.CommentStatus, o.FeaturedMedia, o.Parent = p.CommentStatus, p.FeaturedMediaID, p.ParentID
	if !o.Protected || o.Mine {
		o.Content = content.EditorContent(p)
		o.Excerpt = p.Excerpt
	}
	terms, err := db.PostTerms(c.ctx, s.opts.DB.R, []int64{p.ID})
	if err != nil {
		return o, s.internal(err)
	}
	for _, t := range terms[p.ID] {
		switch t.Taxonomy {
		case db.TaxCategory:
			o.Categories = append(o.Categories, termRef{t.ID, html.UnescapeString(t.Name)})
		case db.TaxTag:
			o.Tags = append(o.Tags, html.UnescapeString(t.Name))
		}
	}
	return o, nil
}

func editPath(p *db.Post) string {
	if p.Type == db.TypePage {
		return "/admin/pages/" + strconv.FormatInt(p.ID, 10) + "/edit"
	}
	return "/admin/posts/" + strconv.FormatInt(p.ID, 10) + "/edit"
}

type listPostsIn struct {
	Type     string `json:"type,omitempty" jsonschema:"post (default) or page"`
	Status   string `json:"status,omitempty" jsonschema:"any (default), publish, draft, pending, future or private; anything but publish lists only the user's own"`
	Search   string `json:"search,omitempty" jsonschema:"text in title, content or excerpt"`
	Category int64  `json:"category,omitempty" jsonschema:"category id"`
	Tag      int64  `json:"tag,omitempty" jsonschema:"tag id"`
	Mine     bool   `json:"mine,omitempty" jsonschema:"only the user's own"`
	Page     int    `json:"page,omitempty" jsonschema:"page number, from 1"`
	PerPage  int    `json:"per_page,omitempty" jsonschema:"1 to 50, default 20"`
}

type listPostsOut struct {
	Posts []postOut `json:"posts"`
	Total int       `json:"total"`
	Page  int       `json:"page"`
	Pages int       `json:"pages"`
}

func postType(t string) (string, error) {
	switch t {
	case "", db.TypePost:
		return db.TypePost, nil
	case db.TypePage:
		return db.TypePage, nil
	}
	return "", errors.New("type must be post or page")
}

func (s *Server) listPosts(ctx context.Context, req *mcp.CallToolRequest, in listPostsIn) (*mcp.CallToolResult, listPostsOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, listPostsOut{}, err
	}
	typ, err := postType(in.Type)
	if err != nil {
		return nil, listPostsOut{}, err
	}
	q := db.AdminPostQuery{Type: typ, Search: in.Search, CategoryID: in.Category, TagID: in.Tag, VisibleTo: c.user.ID}
	switch in.Status {
	case "", "any":
	case db.StatusPublish:
		q.Status = in.Status
	case db.StatusDraft, db.StatusPending, db.StatusFuture, db.StatusPrivate:
		q.Status, q.AuthorID = in.Status, c.user.ID
	default:
		return nil, listPostsOut{}, errors.New("unknown status")
	}
	if in.Mine {
		q.AuthorID = c.user.ID
	}
	per := in.PerPage
	if per <= 0 {
		per = 20
	}
	per = min(per, 50)
	page := max(in.Page, 1)
	q.Limit, q.Offset = per, (page-1)*per
	posts, total, err := db.AdminPosts(c.ctx, s.opts.DB.R, q)
	if err != nil {
		return nil, listPostsOut{}, s.internal(err)
	}
	out := listPostsOut{Posts: []postOut{}, Total: total, Page: page, Pages: (total + per - 1) / per}
	authors := map[int64]string{}
	for i := range posts {
		if !visible(c.user, &posts[i]) {
			continue
		}
		o, err := s.postOut(c, &posts[i], false, authors)
		if err != nil {
			return nil, listPostsOut{}, err
		}
		out.Posts = append(out.Posts, o)
	}
	return nil, out, nil
}

type getPostIn struct {
	ID   int64  `json:"id,omitempty"`
	Slug string `json:"slug,omitempty"`
	Type string `json:"type,omitempty" jsonschema:"post (default) or page, with slug"`
	URL  string `json:"url,omitempty" jsonschema:"a permalink of the site"`
}

// findPost resolves an id, slug or URL to a post the user may read.
func (s *Server) findPost(c *call, id int64, slug, typ, link string) (*db.Post, error) {
	if link != "" {
		u, err := url.Parse(link)
		if err != nil {
			return nil, errors.New("invalid URL")
		}
		for _, k := range []string{"p", "page_id"} {
			if v := u.Query().Get(k); v != "" {
				id, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		if id == 0 {
			slug = path.Base(strings.TrimRight(u.Path, "/"))
			if typ == "" {
				typ = db.TypePost
				if p, err := db.PostBySlug(c.ctx, s.opts.DB.R, db.TypePost, slug); err != nil || !visible(c.user, p) {
					typ = db.TypePage
				}
			}
		}
	}
	var p *db.Post
	var err error
	switch {
	case id > 0:
		p, err = db.GetPost(c.ctx, s.opts.DB.R, id)
	case slug != "":
		t, terr := postType(typ)
		if terr != nil {
			return nil, terr
		}
		p, err = db.PostBySlug(c.ctx, s.opts.DB.R, t, slug)
	default:
		return nil, errors.New("pass id, slug or url")
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!visible(c.user, p) || p.Status == db.StatusTrash)) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, s.internal(err)
	}
	return p, nil
}

func (s *Server) getPost(ctx context.Context, req *mcp.CallToolRequest, in getPostIn) (*mcp.CallToolResult, postOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, postOut{}, err
	}
	p, err := s.findPost(c, in.ID, in.Slug, in.Type, in.URL)
	if err != nil {
		return nil, postOut{}, err
	}
	o, err := s.postOut(c, p, true, nil)
	return nil, o, err
}

type createPostIn struct {
	Type          string   `json:"type,omitempty" jsonschema:"post (default) or page"`
	Title         string   `json:"title"`
	Content       string   `json:"content,omitempty"`
	ContentFormat string   `json:"content_format,omitempty" jsonschema:"markdown (default) or html"`
	Excerpt       string   `json:"excerpt,omitempty" jsonschema:"short summary, plain text"`
	Status        string   `json:"status,omitempty" jsonschema:"draft (default), pending (submit for review), publish or private; publish with a future date schedules"`
	Date          string   `json:"date,omitempty" jsonschema:"publication date in site time, YYYY-MM-DDTHH:MM"`
	Slug          string   `json:"slug,omitempty" jsonschema:"URL name; derived from the title when empty"`
	Categories    []int64  `json:"categories,omitempty" jsonschema:"category ids (see list_terms); posts only"`
	Tags          []string `json:"tags,omitempty" jsonschema:"tag names; unknown tags are created; posts only"`
	FeaturedMedia int64    `json:"featured_media,omitempty" jsonschema:"attachment id of the featured image"`
	CommentStatus string   `json:"comment_status,omitempty" jsonschema:"open or closed"`
	Parent        int64    `json:"parent,omitempty" jsonschema:"parent page id; pages only"`
}

// html converts submitted content to HTML (sanitized later by Save).
func toHTML(text, format string) (string, error) {
	switch format {
	case "", "markdown":
		return render.MarkdownToHTML(text)
	case "html":
		return text, nil
	}
	return "", errors.New("content_format must be markdown or html")
}

func checkStatus(st string) (string, error) {
	switch st {
	case "":
		return db.StatusDraft, nil
	case db.StatusDraft, db.StatusPending, db.StatusPublish, db.StatusPrivate:
		return st, nil
	case db.StatusFuture:
		return db.StatusPublish, nil
	}
	return "", errors.New("status must be draft, pending, publish or private")
}

// statusNote explains a status the agent did not ask for.
func statusNote(asked string, p *db.Post) string {
	if (asked == db.StatusPublish || asked == db.StatusPrivate || asked == db.StatusFuture) && p.Status == db.StatusPending {
		return "The post was submitted for review (pending) because this access token may not publish."
	}
	return ""
}

func (s *Server) createPost(ctx context.Context, req *mcp.CallToolRequest, in createPostIn) (*mcp.CallToolResult, postOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, postOut{}, err
	}
	typ, err := postType(in.Type)
	if err != nil {
		return nil, postOut{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, postOut{}, errors.New("title is required")
	}
	body, err := toHTML(in.Content, in.ContentFormat)
	if err != nil {
		return nil, postOut{}, err
	}
	status, err := checkStatus(in.Status)
	if err != nil {
		return nil, postOut{}, err
	}
	if err := s.checkTerms(c, in.Categories); err != nil {
		return nil, postOut{}, err
	}
	p, err := c.ed.Save(c.ctx, nil, &content.PostInput{Type: typ, Title: in.Title, Content: body, Excerpt: in.Excerpt,
		Status: status, Date: in.Date, Slug: in.Slug, CommentStatus: in.CommentStatus, ParentID: in.Parent,
		Categories: in.Categories, Tags: in.Tags, FeaturedMedia: in.FeaturedMedia})
	if err != nil {
		return nil, postOut{}, s.userError(err)
	}
	o, err := s.postOut(c, p, true, nil)
	o.Note = statusNote(in.Status, p)
	return nil, o, err
}

// checkTerms verifies that ids are categories.
func (s *Server) checkTerms(c *call, ids []int64) error {
	for _, id := range ids {
		t, err := db.GetTerm(c.ctx, s.opts.DB.R, id)
		if err != nil || t == nil || t.Taxonomy != db.TaxCategory {
			return fmt.Errorf("category %d does not exist", id)
		}
	}
	return nil
}

type updatePostIn struct {
	ID               int64    `json:"id"`
	ExpectedModified string   `json:"expected_modified" jsonschema:"the modified value from get_post; the update fails if the post changed since"`
	Title            *string  `json:"title,omitempty"`
	Content          *string  `json:"content,omitempty"`
	ContentFormat    string   `json:"content_format,omitempty" jsonschema:"markdown (default) or html"`
	Excerpt          *string  `json:"excerpt,omitempty"`
	Status           *string  `json:"status,omitempty" jsonschema:"draft, pending, publish or private"`
	Date             *string  `json:"date,omitempty" jsonschema:"publication date in site time, YYYY-MM-DDTHH:MM"`
	Slug             *string  `json:"slug,omitempty"`
	Categories       []int64  `json:"categories,omitempty" jsonschema:"replaces the categories"`
	Tags             []string `json:"tags,omitempty" jsonschema:"replaces the tags"`
	FeaturedMedia    *int64   `json:"featured_media,omitempty" jsonschema:"attachment id, 0 removes it"`
	CommentStatus    *string  `json:"comment_status,omitempty" jsonschema:"open or closed"`
	Parent           *int64   `json:"parent,omitempty"`
}

func (s *Server) updatePost(ctx context.Context, req *mcp.CallToolRequest, in updatePostIn) (*mcp.CallToolResult, postOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, postOut{}, err
	}
	p, err := s.findPost(c, in.ID, "", "", "")
	if err != nil {
		return nil, postOut{}, err
	}
	if !auth.CanEditPost(c.user, p) {
		if p.AuthorID != c.user.ID {
			return nil, postOut{}, errors.New("not allowed: access tokens can only change the user's own posts")
		}
		return nil, postOut{}, errors.New("not allowed: changing a published post needs the content:publish scope")
	}
	if in.ExpectedModified != p.ModifiedGMT.UTC().Format(time.RFC3339) {
		return nil, postOut{}, errors.New("the post was changed since you read it (modified " +
			p.ModifiedGMT.UTC().Format(time.RFC3339) + "); call get_post again and redo your change")
	}
	pi := c.ed.InputFrom(c.ctx, p)
	pi.KeepContent, pi.KeepExcerpt, pi.KeepTerms = true, true, true
	if in.Title != nil {
		pi.Title = *in.Title
	}
	if in.Content != nil {
		if pi.Content, err = toHTML(*in.Content, in.ContentFormat); err != nil {
			return nil, postOut{}, err
		}
		pi.KeepContent = false
	}
	if in.Excerpt != nil {
		pi.Excerpt, pi.KeepExcerpt = *in.Excerpt, false
	}
	asked := ""
	if in.Status != nil {
		asked = *in.Status
		if pi.Status, err = checkStatus(*in.Status); err != nil {
			return nil, postOut{}, err
		}
	}
	if in.Date != nil {
		pi.Date = *in.Date
	}
	if in.Slug != nil {
		pi.Slug = *in.Slug
	}
	if in.FeaturedMedia != nil {
		pi.FeaturedMedia = *in.FeaturedMedia
	}
	if in.CommentStatus != nil {
		pi.CommentStatus = *in.CommentStatus
	}
	if in.Parent != nil {
		pi.ParentID = *in.Parent
	}
	if in.Categories != nil || in.Tags != nil {
		if err := s.checkTerms(c, in.Categories); err != nil {
			return nil, postOut{}, err
		}
		terms, err := db.PostTerms(c.ctx, s.opts.DB.R, []int64{p.ID})
		if err != nil {
			return nil, postOut{}, s.internal(err)
		}
		pi.Categories, pi.Tags = in.Categories, in.Tags
		for _, t := range terms[p.ID] {
			if in.Categories == nil && t.Taxonomy == db.TaxCategory {
				pi.Categories = append(pi.Categories, t.ID)
			}
			if in.Tags == nil && t.Taxonomy == db.TaxTag {
				pi.Tags = append(pi.Tags, t.Name)
			}
		}
		pi.KeepTerms = false
	}
	np, err := c.ed.Save(c.ctx, p, pi)
	if err != nil {
		return nil, postOut{}, s.userError(err)
	}
	o, err := s.postOut(c, np, true, nil)
	o.Note = statusNote(asked, np)
	return nil, o, err
}

// Terms.

type listTermsIn struct {
	Taxonomy string `json:"taxonomy,omitempty" jsonschema:"category (default) or post_tag"`
	Search   string `json:"search,omitempty"`
}

type termOut struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Parent      int64  `json:"parent,omitempty"`
	Count       int    `json:"count" jsonschema:"published posts"`
	Description string `json:"description,omitempty"`
}

type listTermsOut struct {
	Terms []termOut `json:"terms"`
}

func termOf(t *db.Term) termOut {
	return termOut{ID: t.ID, Name: html.UnescapeString(t.Name), Slug: t.Slug, Parent: t.ParentID, Count: t.Count,
		Description: t.Description}
}

func (s *Server) listTerms(ctx context.Context, req *mcp.CallToolRequest, in listTermsIn) (*mcp.CallToolResult, listTermsOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, listTermsOut{}, err
	}
	tax := in.Taxonomy
	switch tax {
	case "", "category":
		tax = db.TaxCategory
	case "post_tag", "tag":
		tax = db.TaxTag
	default:
		return nil, listTermsOut{}, errors.New("taxonomy must be category or post_tag")
	}
	terms, err := db.AdminTerms(c.ctx, s.opts.DB.R, db.AdminTermQuery{Taxonomy: tax, Search: in.Search})
	if err != nil {
		return nil, listTermsOut{}, s.internal(err)
	}
	out := listTermsOut{Terms: []termOut{}}
	for i := range terms {
		if len(out.Terms) == 500 {
			break
		}
		out.Terms = append(out.Terms, termOf(&terms[i]))
	}
	return nil, out, nil
}

type createCategoryIn struct {
	Name        string `json:"name"`
	Parent      int64  `json:"parent,omitempty" jsonschema:"parent category id"`
	Description string `json:"description,omitempty"`
}

func (s *Server) createCategory(ctx context.Context, req *mcp.CallToolRequest, in createCategoryIn) (*mcp.CallToolResult, termOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, termOut{}, err
	}
	t, err := s.opts.Content.SaveTerm(c.ctx, c.user, nil, content.TermInput{Taxonomy: db.TaxCategory, Name: in.Name,
		Description: in.Description, ParentID: in.Parent})
	if err != nil {
		return nil, termOut{}, s.userError(err)
	}
	return nil, termOf(t), nil
}

// Media.

// uploadTypes are the file types agents may upload: images and PDF, not
// archives or executables (an agent misled by a prompt injection must not
// turn the site into a download host).
var uploadTypes = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".pdf": true}

func (s *Server) uploadLimit(st *site.Settings) int64 { return min(st.UploadMaxBytes, s.maxUpload) }

type uploadMediaIn struct {
	Filename string `json:"filename" jsonschema:"file name with extension, e.g. screenshot.png"`
	Data     string `json:"data" jsonschema:"file content, base64 encoded"`
	Alt      string `json:"alt,omitempty" jsonschema:"alternative text for images"`
	Caption  string `json:"caption,omitempty"`
	Title    string `json:"title,omitempty"`
	AttachTo int64  `json:"attach_to,omitempty" jsonschema:"id of the user's post the file belongs to"`
}

type sizeOut struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type mediaOut struct {
	ID       int64              `json:"id"`
	Title    string             `json:"title"`
	URL      string             `json:"url"`
	Mime     string             `json:"mime"`
	Width    int                `json:"width,omitempty"`
	Height   int                `json:"height,omitempty"`
	Filesize int64              `json:"filesize"`
	Alt      string             `json:"alt,omitempty"`
	Sizes    map[string]sizeOut `json:"sizes,omitempty"`
	HTML     string             `json:"html,omitempty" jsonschema:"markup to insert into post content"`
	Date     string             `json:"date"`
}

func sizeURL(l *site.Links, m *db.Media, name string) string {
	file := m.File
	if sz, ok := m.Sizes[name]; ok && sz.File != "" {
		file = path.Join(path.Dir(m.File), sz.File)
	}
	return l.UploadURL(file)
}

func mediaOf(c *call, p *db.Post, m *db.Media) mediaOut {
	o := mediaOut{ID: p.ID, Title: html.UnescapeString(p.Title), URL: c.links.UploadURL(m.File), Mime: m.Mime, Width: m.Width,
		Height: m.Height, Filesize: m.Filesize, Alt: html.UnescapeString(m.Alt), Date: p.Date.Format("2006-01-02")}
	if !strings.HasPrefix(m.Mime, "image/") {
		o.HTML = `<a href="` + html.EscapeString(o.URL) + `">` + html.EscapeString(o.Title) + `</a>`
		return o
	}
	o.Sizes = map[string]sizeOut{}
	for name, sz := range m.Sizes {
		o.Sizes[name] = sizeOut{sizeURL(c.links, m, name), sz.Width, sz.Height}
	}
	size, w, h := "full", m.Width, m.Height
	if sz, ok := m.Sizes["large"]; ok {
		size, w, h = "large", sz.Width, sz.Height
	}
	o.HTML = fmt.Sprintf(`<img src="%s" alt="%s" width="%d" height="%d" class="size-%s wp-image-%d">`,
		html.EscapeString(sizeURL(c.links, m, size)), html.EscapeString(o.Alt), w, h, size, p.ID)
	return o
}

func (s *Server) uploadMedia(ctx context.Context, req *mcp.CallToolRequest, in uploadMediaIn) (*mcp.CallToolResult, mediaOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, mediaOut{}, err
	}
	if !auth.Can(c.user, auth.CapUploadFiles) {
		return nil, mediaOut{}, s.userError(content.ErrForbidden)
	}
	name := path.Base(strings.ReplaceAll(in.Filename, `\`, "/"))
	if !uploadTypes[strings.ToLower(path.Ext(name))] {
		return nil, mediaOut{}, errors.New("file type not allowed: upload JPEG, PNG, GIF, WebP or PDF")
	}
	data := in.Data
	if strings.HasPrefix(data, "data:") { // data: URL
		if _, after, ok := strings.Cut(data, ","); ok {
			data = after
		}
	}
	data = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, data)
	limit := s.uploadLimit(c.st)
	if int64(base64.StdEncoding.DecodedLen(len(data))) > limit+3 {
		return nil, mediaOut{}, fmt.Errorf("the file is larger than %d bytes", limit)
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(data); err != nil {
			return nil, mediaOut{}, errors.New("data is not valid base64")
		}
	}
	if len(b) == 0 || int64(len(b)) > limit {
		return nil, mediaOut{}, fmt.Errorf("the file is empty or larger than %d bytes", limit)
	}
	store := s.opts.Content.MediaStore()
	if store == nil {
		return nil, mediaOut{}, errors.New("uploads are not available")
	}
	f, tmp, err := store.CreateTemp()
	if err != nil {
		return nil, mediaOut{}, s.internal(err)
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		store.RemoveTemp(tmp)
		return nil, mediaOut{}, s.internal(err)
	}
	p, m, err := c.ed.Upload(c.ctx, tmp, name, in.AttachTo)
	if err != nil {
		return nil, mediaOut{}, s.userError(err)
	}
	var ai content.AttachmentInput
	if in.Alt != "" {
		ai.Alt = &in.Alt
	}
	if in.Caption != "" {
		ai.Caption = &in.Caption
	}
	if in.Title != "" {
		ai.Title = &in.Title
	}
	if ai.Alt != nil || ai.Caption != nil || ai.Title != nil {
		if p, m, err = c.ed.UpdateAttachment(c.ctx, p.ID, ai); err != nil {
			return nil, mediaOut{}, s.userError(err)
		}
	}
	return nil, mediaOf(c, p, m), nil
}

type listMediaIn struct {
	Search  string `json:"search,omitempty"`
	Type    string `json:"type,omitempty" jsonschema:"image, audio, video or document"`
	Page    int    `json:"page,omitempty"`
	PerPage int    `json:"per_page,omitempty" jsonschema:"1 to 50, default 20"`
}

type listMediaOut struct {
	Media []mediaOut `json:"media"`
	Total int        `json:"total"`
	Page  int        `json:"page"`
	Pages int        `json:"pages"`
}

func (s *Server) listMedia(ctx context.Context, req *mcp.CallToolRequest, in listMediaIn) (*mcp.CallToolResult, listMediaOut, error) {
	c, err := s.begin(ctx, req)
	if err != nil {
		return nil, listMediaOut{}, err
	}
	if !auth.Can(c.user, auth.CapUploadFiles) {
		return nil, listMediaOut{}, s.userError(content.ErrForbidden)
	}
	switch in.Type {
	case "", "image", "audio", "video", "document":
	default:
		return nil, listMediaOut{}, errors.New("type must be image, audio, video or document")
	}
	per := in.PerPage
	if per <= 0 {
		per = 20
	}
	per = min(per, 50)
	page := max(in.Page, 1)
	posts, media, total, err := db.AdminMedia(c.ctx, s.opts.DB.R, db.MediaQuery{Search: in.Search, Type: in.Type,
		Limit: per, Offset: (page - 1) * per})
	if err != nil {
		return nil, listMediaOut{}, s.internal(err)
	}
	out := listMediaOut{Media: []mediaOut{}, Total: total, Page: page, Pages: (total + per - 1) / per}
	for i := range posts {
		if m := media[posts[i].ID]; m != nil {
			out.Media = append(out.Media, mediaOf(c, &posts[i], m))
		}
	}
	return nil, out, nil
}
