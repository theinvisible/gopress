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

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/live"
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
		return map[string]any{"name": html.UnescapeString(st.Name), "tagline": html.UnescapeString(st.Description),
			"url": st.Home + "/", "language": st.Lang, "timezone": tz, "content_width": st.ContentWidth}, nil

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
		if a.Kind == "option" {
			// Only the options granted one by one.
			all, err := db.StashEntries(ctx, h.opts.DB.R, "wordpress", "option")
			if err != nil {
				return nil, err
			}
			out := map[string]json.RawMessage{}
			for k, v := range all {
				if p.can(DataImportOption + k) {
					out[k] = v
				}
			}
			return out, nil
		}
		if strings.HasPrefix(a.Kind, "option:") || !p.can(DataImportPrefix+a.Kind) {
			return nil, errDenied
		}
		return db.StashEntries(ctx, h.opts.DB.R, "wordpress", a.Kind)

	case "mail_admins":
		if !p.can(DataMailAdmins) {
			return nil, errDenied
		}
		a, err := args[struct{ Subject, Body string }](raw)
		if err != nil {
			return nil, err
		}
		return true, p.mailAdmins(ctx, a.Subject, a.Body)

	case "notify_admins":
		if !p.can(DataNotifyAdmins) {
			return nil, errDenied
		}
		a, err := args[struct{ Text, Kind, Capability string }](raw)
		if err != nil {
			return nil, err
		}
		return true, p.notifyAdmins(a.Text, a.Kind, a.Capability)

	case "live_publish":
		a, err := args[struct {
			Page string
			Data json.RawMessage
		}](raw)
		if err != nil {
			return nil, err
		}
		return true, p.livePublish(a.Page, a.Data)

	case "progress":
		a, err := args[struct {
			Percent int
			Text    string
		}](raw)
		if err != nil {
			return nil, err
		}
		if ctx.Value(migratingKey{}) == nil {
			return nil, userErr("progress is only for the WordPress import")
		}
		if h.progressLimit.allow(name, h.now()) {
			h.publish(live.Event{Type: "import.progress", Capability: auth.CapActivatePlugins,
				Data: map[string]any{"plugin": name, "percent": min(max(a.Percent, 0), 100), "text": plainText(a.Text, 200)}})
		}
		return true, nil

	case "settings_set":
		if ctx.Value(migratingKey{}) == nil {
			return nil, userErr("settings can only be set during the WordPress import")
		}
		a, err := args[struct {
			Page   string
			Values map[string]any
		}](raw)
		if err != nil {
			return nil, err
		}
		return true, h.ImportSettings(ctx, name, a.Page, a.Values)

	case "templates":
		a, err := args[struct{ Version string }](raw)
		if err != nil {
			return nil, err
		}
		t, err := p.templates()
		if err != nil {
			return nil, err
		}
		if a.Version == t.Version {
			return templateFiles{Version: t.Version}, nil // the plugin's copy is current
		}
		return t, nil

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
		if err != nil || a.Type != db.TypeAttachment || a.Status == db.StatusTrash || a.Status == db.StatusPrivate {
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
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	TimeoutMS int               `json:"timeout_ms"` // 100–10000; 0 = httpTimeout
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
	timeout := httpTimeout
	if a.TimeoutMS != 0 {
		if a.TimeoutMS < 100 || a.TimeoutMS > int(httpTimeout/time.Millisecond) {
			return nil, userErr("timeout_ms must be 100–10000")
		}
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	select {
	case p.http <- struct{}{}:
		defer func() { <-p.http }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// The call's own deadline still applies (a hook cannot outlive its
	// time limit by waiting for a slow server).
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
	client.Timeout = timeout
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
		// The error names the URL: without its query (it may hold secrets).
		msg := err.Error()
		var ue *url.Error
		if errors.As(err, &ue) {
			msg = ue.Err.Error()
		}
		return nil, userErr("request failed: " + truncate(msg, 200))
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

// Mail to administrators.
const (
	maxMailSubject = 150
	maxMailBody    = 10 << 10
	mailPerHour    = 20
)

// mailAdmins queues a plain-text message from the plugin to every user
// who may manage options. The recipients are fixed by the host; the
// subject is one line and carries the site and plugin name (template
// plugin_notice).
func (p *plugin) mailAdmins(ctx context.Context, subject, body string) error {
	h := p.h
	if h.opts.Mail == nil || !h.opts.Mail.Ready(ctx) {
		return userErr("mail is not configured")
	}
	subject = strings.Join(strings.Fields(subject), " ")
	if subject == "" || utf8.RuneCountInString(subject) > maxMailSubject || !utf8.ValidString(subject) {
		return userErr("the subject must be 1–150 characters")
	}
	if body == "" || len(body) > maxMailBody || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return userErr("the body must be plain text of at most 10 KB")
	}
	if !h.mailLimit.allow(p.pkg.Manifest.Name, h.now()) {
		return userErr("mail limit reached (20 an hour)")
	}
	users, err := db.Users(ctx, h.opts.DB.R)
	if err != nil {
		return err
	}
	sent := 0
	for i := range users {
		if !auth.Can(&users[i], auth.CapManageOptions) || users[i].Email == "" {
			continue
		}
		data := map[string]any{"Plugin": p.pkg.Manifest.Title, "Subject": subject, "Body": body}
		if err := h.opts.Mail.Enqueue(ctx, users[i].Email, "plugin_notice", data); err != nil {
			h.log.Warn("plugin mail", "plugin", p.pkg.Manifest.Name, "err", err)
			continue
		}
		sent++
	}
	d, _ := json.Marshal(map[string]any{"plugin": p.pkg.Manifest.Name, "subject": subject, "recipients": sent})
	db.InsertAudit(ctx, h.opts.DB.W, &db.AuditEntry{Time: h.now().Unix(), Action: "plugin_mail", ObjectType: "plugin",
		Detail: string(d)})
	return nil
}

// Live events.
const (
	noticesPerMin  = 30
	eventsPerMin   = 120
	maxNoticeText  = 300
	maxPluginEvent = 16 << 10
)

// migratingKey marks the context of a migrate_wordpress call.
type migratingKey struct{}

func (h *Host) publish(e live.Event) {
	if h.opts.Live != nil {
		h.opts.Live.Publish(e)
	}
}

// plainText collapses whitespace and cuts s to max runes.
func plainText(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// notifyAdmins shows a live notice to the users with capability (one of
// the admin capabilities plugin pages use). The text is plain; the admin
// shows it with the plugin's title.
func (p *plugin) notifyAdmins(text, kind, capability string) error {
	text = plainText(text, maxNoticeText)
	if text == "" {
		return userErr("the text is empty")
	}
	if kind == "" {
		kind = "info"
	}
	if !slices.Contains([]string{"success", "info", "warning", "error"}, kind) {
		return userErr("kind must be success, info, warning or error")
	}
	if capability == "" {
		capability = auth.CapManageOptions
	}
	if !slices.Contains(adminCapabilities, capability) {
		return userErr("capability must be one of " + strings.Join(adminCapabilities, ", "))
	}
	if !p.h.noticeLimit.allow(p.pkg.Manifest.Name, p.h.now()) {
		return userErr("notice limit reached (30 a minute)")
	}
	p.h.publish(live.Event{Type: "notice", Capability: capability,
		Data: map[string]any{"text": text, "kind": kind, "source": p.pkg.Manifest.Title}})
	return nil
}

// livePublish sends data to the plugin's own iframe page (gopress.on in
// the page), to users who may open that page.
func (p *plugin) livePublish(page string, data json.RawMessage) error {
	var pg *AdminPage
	for i := range p.pkg.Manifest.Admin {
		if a := &p.pkg.Manifest.Admin[i]; a.ID == page && a.Kind == "iframe" {
			pg = a
		}
	}
	if pg == nil {
		return userErr("page must be one of the plugin's iframe pages")
	}
	if len(data) == 0 || len(data) > maxPluginEvent || !json.Valid(data) {
		return userErr("data must be JSON of at most 16 KB")
	}
	if !p.h.eventLimit.allow(p.pkg.Manifest.Name, p.h.now()) {
		return userErr("event limit reached (120 a minute)")
	}
	ev := map[string]any{"plugin": p.pkg.Manifest.Name, "page": page, "data": data}
	// The size as sent (JSON escapes <, > and & to six bytes each).
	if b, err := json.Marshal(ev); err != nil || len(b) > live.MaxEventBytes {
		return userErr("data must be JSON of at most 16 KB")
	}
	p.h.publish(live.Event{Type: "plugin.event", Topic: live.PluginTopic(p.pkg.Manifest.Name, page), Capability: pg.Capability, Data: ev})
	return nil
}
