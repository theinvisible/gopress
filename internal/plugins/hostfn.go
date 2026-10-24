package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	extism "github.com/extism/go-sdk"

	"github.com/theinvisible/gopress/internal/db"
)

// Host function limits.
const (
	maxValueBytes   = 1 << 20  // one KV value
	kvQuota         = 50 << 20 // all KV data of a plugin
	maxHTTPBody     = 1 << 20  // request body
	maxHTTPResponse = 2 << 20
	httpTimeout     = 10 * time.Second
)

var (
	reColl = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	reHdr  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// hostFunction is the single import plugins call the host through:
// extism:host/user.gopress(request) → response, both JSON.
func (p *plugin) hostFunction() extism.HostFunction {
	f := extism.NewHostFunctionWithStack("gopress", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		resp := p.dispatch(ctx, cp, stack[0])
		off, err := cp.WriteBytes(resp)
		if err != nil {
			off = 0
		}
		stack[0] = off
	}, []extism.ValueType{extism.ValueTypePTR}, []extism.ValueType{extism.ValueTypePTR})
	f.SetNamespace("extism:host/user")
	return f
}

type hostReq struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

// errDenied is returned for operations the plugin has no permission for.
var errDenied = errors.New("permission denied")

func (p *plugin) dispatch(ctx context.Context, cp *extism.CurrentPlugin, off uint64) (resp []byte) {
	defer func() {
		if r := recover(); r != nil {
			p.h.log.Error("plugin host function panicked", "plugin", p.pkg.Manifest.Name, "panic", r)
			resp = []byte(`{"error":"internal error"}`)
		}
	}()
	n, err := cp.Length(off)
	if err != nil || n > maxInputBytes {
		return []byte(`{"error":"request too large"}`)
	}
	raw, err := cp.ReadBytes(off)
	if err != nil {
		return []byte(`{"error":"invalid request"}`)
	}
	var req hostReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return []byte(`{"error":"invalid request"}`)
	}
	result, err := p.op(ctx, req.Op, req.Args)
	if err != nil {
		var ue userErr
		msg := "internal error"
		switch {
		case errors.As(err, &ue), errors.Is(err, errDenied):
			msg = err.Error()
		default:
			p.h.log.Error("plugin host function", "plugin", p.pkg.Manifest.Name, "op", req.Op, "err", err)
		}
		b, _ := json.Marshal(map[string]string{"error": msg})
		return b
	}
	b, err := json.Marshal(map[string]any{"result": result})
	if err != nil {
		return []byte(`{"error":"internal error"}`)
	}
	return b
}

// userErr is an error message for the plugin.
type userErr string

func (e userErr) Error() string { return string(e) }

func (p *plugin) can(data string) bool { return slices.Contains(p.perms.Data, data) }

func args[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, userErr("invalid arguments: " + err.Error())
		}
	}
	return v, nil
}

func (p *plugin) op(ctx context.Context, op string, raw json.RawMessage) (any, error) {
	h, name := p.h, p.pkg.Manifest.Name
	switch op {
	case "log":
		a, err := args[struct{ Level, Message string }](raw)
		if err != nil {
			return nil, err
		}
		level := slog.LevelInfo
		switch a.Level {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		p.logLine(level, a.Message)
		return true, nil

	case "site_info":
		st, err := h.opts.Site.Settings(ctx)
		if err != nil {
			return nil, err
		}
		tz := "UTC"
		if st.Loc != nil {
			tz = st.Loc.String()
		}
		return map[string]string{"name": html.UnescapeString(st.Name), "tagline": html.UnescapeString(st.Description),
			"url": st.Home + "/", "language": st.Lang, "timezone": tz}, nil

	case "kv_get":
		a, err := args[struct{ Collection, Key string }](raw)
		if err != nil {
			return nil, err
		}
		if err := checkKV(a.Collection, a.Key); err != nil {
			return nil, err
		}
		v, ok, err := db.KVGet(ctx, h.opts.DB.R, name, a.Collection, a.Key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return map[string]any{"found": false}, nil
		}
		return map[string]any{"found": true, "value": json.RawMessage(v)}, nil

	case "kv_set":
		a, err := args[struct {
			Collection, Key string
			Value           json.RawMessage
		}](raw)
		if err != nil {
			return nil, err
		}
		if err := checkKV(a.Collection, a.Key); err != nil {
			return nil, err
		}
		if len(a.Value) > maxValueBytes || !json.Valid(a.Value) || len(a.Value) == 0 {
			return nil, userErr("the value must be JSON of at most 1 MB")
		}
		err = h.opts.DB.Tx(ctx, func(tx *sql.Tx) error {
			return db.KVSet(ctx, tx, name, a.Collection, a.Key, string(a.Value), h.now().Unix(), kvQuota)
		})
		if errors.Is(err, db.ErrKVQuota) {
			return nil, userErr("storage quota of 50 MB exceeded")
		}
		return err == nil, err

	case "kv_delete":
		a, err := args[struct{ Collection, Key string }](raw)
		if err != nil {
			return nil, err
		}
		if err := checkKV(a.Collection, a.Key); err != nil {
			return nil, err
		}
		return true, db.KVDelete(ctx, h.opts.DB.W, name, a.Collection, a.Key)

	case "kv_list":
		a, err := args[struct {
			Collection, After string
			Limit             int
		}](raw)
		if err != nil {
			return nil, err
		}
		if !reColl.MatchString(a.Collection) || strings.HasPrefix(a.Collection, "_") {
			return nil, userErr("invalid collection")
		}
		limit := min(max(a.Limit, 1), 500)
		entries, err := db.KVList(ctx, h.opts.DB.R, name, a.Collection, a.After, limit)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, map[string]any{"key": e.Key, "value": json.RawMessage(e.Value)})
		}
		next := ""
		if len(entries) == limit {
			next = entries[len(entries)-1].Key
		}
		return map[string]any{"entries": out, "next": next}, nil

	case "kv_count":
		a, err := args[struct{ Collection string }](raw)
		if err != nil {
			return nil, err
		}
		if !reColl.MatchString(a.Collection) || strings.HasPrefix(a.Collection, "_") {
			return nil, userErr("invalid collection")
		}
		return db.KVCount(ctx, h.opts.DB.R, name, a.Collection)

	case "settings_get":
		a, err := args[struct{ Page string }](raw)
		if err != nil {
			return nil, err
		}
		return h.Settings(ctx, name, a.Page)

	case "posts_query":
		if !p.can(DataPostsRead) {
			return nil, errDenied
		}
		a, err := args[struct {
			Type, Search  string
			Limit, Offset int
		}](raw)
		if err != nil {
			return nil, err
		}
		typ := db.TypePost
		if a.Type == db.TypePage {
			typ = db.TypePage
		}
		posts, total, err := db.AdminPosts(ctx, h.opts.DB.R, db.AdminPostQuery{Type: typ, Status: db.StatusPublish, Search: a.Search, NoPassword: true,
			Limit: min(max(a.Limit, 1), 100), Offset: max(a.Offset, 0)})
		if err != nil {
			return nil, err
		}
		links, err := h.opts.Site.Links(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(posts))
		for i := range posts {
			if posts[i].Password != "" {
				continue
			}
			out = append(out, postData(&posts[i], links.PostURL(&posts[i]), false))
		}
		return map[string]any{"posts": out, "total": total}, nil

	case "post_get":
		if !p.can(DataPostsRead) {
			return nil, errDenied
		}
		a, err := args[struct{ ID int64 }](raw)
		if err != nil {
			return nil, err
		}
		post, err := db.GetPost(ctx, h.opts.DB.R, a.ID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (post.Status != db.StatusPublish || post.Password != "" ||
			(post.Type != db.TypePost && post.Type != db.TypePage))) {
			return nil, userErr("not found")
		}
		if err != nil {
			return nil, err
		}
		links, err := h.opts.Site.Links(ctx)
		if err != nil {
			return nil, err
		}
		return postData(post, links.PostURL(post), true), nil

	case "media_get":
		if !p.can(DataMediaRead) {
			return nil, errDenied
		}
		a, err := args[struct{ IDs []int64 }](raw)
		if err != nil {
			return nil, err
		}
		if len(a.IDs) > 200 {
			return nil, userErr("at most 200 ids")
		}
		return h.mediaData(ctx, a.IDs)

	case "comment_get":
		if !p.can(DataCommentsRead) && !p.can(DataCommentsModify) {
			return nil, errDenied
		}
		a, err := args[struct{ ID int64 }](raw)
		if err != nil {
			return nil, err
		}
		c, err := db.GetComment(ctx, h.opts.DB.R, a.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, userErr("not found")
		}
		if err != nil {
			return nil, err
		}
		return commentData(c), nil

	case "comment_set_status":
		if !p.can(DataCommentsModify) {
			return nil, errDenied
		}
		a, err := args[struct {
			ID     int64
			Status string
		}](raw)
		if err != nil {
			return nil, err
		}
		return true, h.setCommentStatus(ctx, name, a.ID, a.Status)

	case "http_request":
		a, err := args[httpReq](raw)
		if err != nil {
			return nil, err
		}
		return p.httpRequest(ctx, a)

	case "stash_list":
		a, err := args[struct{ Kind string }](raw)
		if err != nil {
			return nil, err
		}
		if !p.can(DataImportPrefix + a.Kind) {
			return nil, errDenied
		}
		return db.StashEntries(ctx, h.opts.DB.R, "wordpress", a.Kind)

	case "invalidate":
		// At most once a second per plugin: clearing the cache on every
		// request would turn every page view into a render.
		if h.invalidateLimit.allow(name, h.now()) {
			h.opts.Site.Invalidate()
		}
		return true, nil
	}
	return nil, userErr("unknown operation " + strconv.Quote(op))
}

func checkKV(coll, key string) error {
	if !reColl.MatchString(coll) || strings.HasPrefix(coll, "_") {
		return userErr("invalid collection (a-z, 0-9, _ and -, at most 40)")
	}
	if key == "" || len(key) > 200 || !utf8.ValidString(key) || strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 }) {
		return userErr("invalid key (1–200 characters)")
	}
	return nil
}

func postData(p *db.Post, link string, full bool) map[string]any {
	m := map[string]any{"id": p.ID, "type": p.Type, "title": html.UnescapeString(p.Title), "slug": p.Slug, "url": link,
		"date": p.Date.Format("2006-01-02T15:04:05"), "excerpt": p.Excerpt}
	if full {
		m["content"] = p.Content
	}
	return m
}

func commentData(c *db.Comment) CommentData {
	return CommentData{ID: c.ID, PostID: c.PostID, ParentID: c.ParentID, UserID: c.UserID, Author: c.Author, Email: c.AuthorEmail,
		URL: c.AuthorURL, IP: c.AuthorIP, Agent: c.Agent, Content: c.Content, Type: c.Type, Status: c.Status,
		Date: c.DateGMT.UTC().Format(time.RFC3339)}
}

// mediaData describes attachments of published posts and unattached ones.
func (h *Host) mediaData(ctx context.Context, ids []int64) ([]map[string]any, error) {
	links, err := h.opts.Site.Links(ctx)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, id := range ids {
		a, err := db.GetPost(ctx, h.opts.DB.R, id)
		if err != nil || a.Type != db.TypeAttachment {
			continue
		}
		if a.ParentID != 0 {
			parent, err := db.GetPost(ctx, h.opts.DB.R, a.ParentID)
			if err != nil || parent.Status != db.StatusPublish || parent.Password != "" {
				continue
			}
		}
		m, err := db.GetMedia(ctx, h.opts.DB.R, id)
		if err != nil || m == nil {
			continue
		}
		sizes := map[string]any{}
		dir := path.Dir(m.File)
		for n, s := range m.Sizes {
			sizes[n] = map[string]any{"url": links.UploadURL(path.Join(dir, s.File)), "width": s.Width, "height": s.Height}
		}
		out = append(out, map[string]any{"id": a.ID, "url": links.UploadURL(m.File), "mime": m.Mime, "width": m.Width,
			"height": m.Height, "alt": html.UnescapeString(m.Alt), "title": html.UnescapeString(a.Title), "caption": a.Excerpt,
			"description": a.Content, "sizes": sizes})
	}
	return out, nil
}

func (h *Host) setCommentStatus(ctx context.Context, plugin string, id int64, status string) error {
	target := map[string]string{"approve": db.CommentApproved, "hold": db.CommentPending, "spam": db.CommentSpam,
		"trash": db.CommentTrash}[status]
	if target == "" {
		return userErr("status must be approve, hold, spam or trash")
	}
	c, err := db.GetComment(ctx, h.opts.DB.R, id)
	if errors.Is(err, sql.ErrNoRows) {
		return userErr("not found")
	}
	if err != nil {
		return err
	}
	err = h.opts.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.SetCommentStatus(ctx, tx, id, target); err != nil {
			return err
		}
		return db.UpdateCommentCount(ctx, tx, c.PostID)
	})
	if err != nil {
		return err
	}
	d, _ := json.Marshal(map[string]any{"plugin": plugin, "status": target})
	db.InsertAudit(ctx, h.opts.DB.W, &db.AuditEntry{Time: h.now().Unix(), Action: "plugin_comment_status", ObjectType: "comment",
		ObjectID: id, Detail: string(d)})
	h.opts.Site.Invalidate()
	return nil
}

type httpReq struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// blockedRequestHeaders are set by the host.
var blockedRequestHeaders = []string{"host", "connection", "content-length", "transfer-encoding", "te", "trailer", "upgrade",
	"proxy-authorization", "proxy-connection", "keep-alive", "expect"}

func (p *plugin) allowedURL(u *url.URL) bool {
	return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") && u.User == nil &&
		slices.Contains(p.perms.HTTP, strings.ToLower(u.Hostname()))
}

func (p *plugin) httpRequest(ctx context.Context, a httpReq) (any, error) {
	if len(p.perms.HTTP) == 0 {
		return nil, errDenied
	}
	u, err := url.Parse(a.URL)
	if err != nil || !p.allowedURL(u) {
		return nil, userErr("URL not allowed: only https to " + strings.Join(p.perms.HTTP, ", "))
	}
	method := strings.ToUpper(a.Method)
	if method == "" {
		method = http.MethodGet
	}
	if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}, method) {
		return nil, userErr("method not allowed")
	}
	if len(a.Body) > maxHTTPBody {
		return nil, userErr("request body too large")
	}
	select {
	case p.http <- struct{}{}:
		defer func() { <-p.http }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(a.Body))
	if err != nil {
		return nil, userErr("invalid request")
	}
	if len(a.Headers) > 50 {
		return nil, userErr("too many headers")
	}
	for k, v := range a.Headers {
		if !reHdr.MatchString(k) || slices.Contains(blockedRequestHeaders, strings.ToLower(k)) ||
			strings.ContainsAny(v, "\r\n\x00") || len(v) > 4096 {
			return nil, userErr("header " + strconv.Quote(k) + " not allowed")
		}
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "GoPress/"+p.pkg.Manifest.Name)
	}
	client := *p.h.http
	client.Timeout = httpTimeout
	client.Jar = nil
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !p.allowedURL(r.URL) {
			return fmt.Errorf("redirect to %s not allowed", r.URL.Host)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, userErr("request failed: " + truncate(err.Error(), 200))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponse+1))
	if err != nil {
		return nil, userErr("reading the response failed")
	}
	if len(body) > maxHTTPResponse {
		return nil, userErr("response larger than 2 MB")
	}
	hdr := map[string]string{}
	for k, v := range resp.Header {
		if len(hdr) < 50 {
			hdr[strings.ToLower(k)] = truncate(strings.Join(v, ", "), 4096)
		}
	}
	if !utf8.Valid(body) {
		body = bytes.ToValidUTF8(body, []byte("�"))
	}
	return map[string]any{"status": resp.StatusCode, "headers": hdr, "body": string(body)}, nil
}
