package site

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theinvisible/gopress/internal/antispam"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
	"github.com/theinvisible/gopress/internal/render"
)

const maxCommentBody = 64 << 10

// CommentFilter lets plugins (Akismet) inspect a new comment before it is
// stored. It returns the status to store ("" keeps the proposed one) or
// "discard" to drop the comment silently.
type CommentFilter func(r *http.Request, c *db.Comment, post *db.Post) string

// SetCommentFilter installs the comment filter (used by the plugin host).
func (s *Site) SetCommentFilter(f CommentFilter) { s.commentFilter = f }

// CommentNotifier is called after a visitor's comment was stored (email
// notifications).
type CommentNotifier func(ctx context.Context, c *db.Comment, approved bool)

// SetCommentNotifier installs the notifier.
func (s *Site) SetCommentNotifier(f CommentNotifier) { s.commentNotify = f }

// commentError renders a wp_die()-style error page.
func commentError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"en-US\"><head><meta charset=\"UTF-8\"><meta name=\"robots\" content=\"noindex\"><title>Comment Submission Failure</title></head>"+
		"<body><p><strong>Error:</strong> %s</p><p>Please use your browser's back button to return.</p></body></html>", html.EscapeString(msg))
}

// commentToken returns a fresh form token for a post (comment-guard.js).
func (s *Site) commentToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	st, err := s.current(r.Context())
	if err != nil {
		http.Error(w, `{}`, http.StatusInternalServerError)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("post"), 10, 64)
	p := st.ix.posts[id]
	if p == nil || !st.ix.isPublic(p) || !st.s.CommentsOpen(p) {
		http.Error(w, `{}`, http.StatusNotFound)
		return
	}
	fmt.Fprintf(w, `{"token":%q}`, s.guard.Token(id, time.Now()))
}

// postComment handles POST /wp-comments-post.php.
func (s *Site) postComment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommentBody)
	if err := r.ParseForm(); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			commentError(w, http.StatusRequestEntityTooLarge, "Your comment is too long.")
			return
		}
		commentError(w, http.StatusBadRequest, "Invalid request.")
		return
	}
	st, err := s.current(r.Context())
	if err != nil {
		commentError(w, http.StatusInternalServerError, "Please try again later.")
		return
	}
	if !httpx.SameOrigin(r, st.s.Home) {
		commentError(w, http.StatusForbidden, "Comments must be posted from this site.")
		return
	}
	f := r.PostForm
	postID, _ := strconv.ParseInt(f.Get("comment_post_ID"), 10, 64)
	post := st.ix.posts[postID]
	if post == nil || !st.ix.isPublic(post) || post.Password != "" {
		commentError(w, http.StatusNotFound, "Sorry, comments are not allowed on this item.")
		return
	}
	if !st.s.CommentsOpen(post) {
		commentError(w, http.StatusForbidden, "Sorry, comments are closed for this item.")
		return
	}

	ip := httpx.ClientIP(r, s.opts.TrustedProxies)
	switch reason := s.guard.Check(f, postID, ip, time.Now()); reason {
	case antispam.OK:
	case antispam.RateLimited:
		s.log.Info("comment rejected", "reason", reason, "ip", ip)
		commentError(w, http.StatusTooManyRequests, "You are posting comments too quickly. Slow down.")
		return
	default:
		s.log.Info("comment rejected", "reason", reason, "ip", ip)
		commentError(w, http.StatusBadRequest, "Your comment could not be verified. Please reload the page and try again.")
		return
	}

	// Logged-in users comment under their account: only from this site
	// (SameSite=Strict session cookie, origin and the session's CSRF
	// token in the form).
	vis := s.formVisitor(r, st.s.Home)
	if st.s.CommentRegistration && vis == nil {
		commentError(w, http.StatusForbidden, "Sorry, you must be logged in to comment.")
		return
	}
	author := truncateRunes(strings.TrimSpace(render.SanitizeText(f.Get("author"))), 245)
	email := truncateRunes(strings.TrimSpace(f.Get("email")), 100)
	website := truncateRunes(strings.TrimSpace(f.Get("url")), 200)
	if vis != nil {
		author, email, website = truncateRunes(vis.DisplayName, 245), vis.Email, vis.URL
	}
	content := strings.TrimSpace(f.Get("comment"))
	if !utf8.ValidString(author + email + website + content) {
		commentError(w, http.StatusBadRequest, "Invalid characters.")
		return
	}
	if st.s.RequireNameEmail && vis == nil {
		if author == "" || email == "" {
			commentError(w, http.StatusBadRequest, "Please fill the required fields.")
			return
		}
	}
	if email != "" {
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
			commentError(w, http.StatusBadRequest, "Please enter a valid email address.")
			return
		}
	}
	if website != "" {
		if !strings.Contains(website, "://") {
			website = "http://" + website
		}
		if u, err := url.Parse(website); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			website = ""
		}
	}
	if content == "" {
		commentError(w, http.StatusBadRequest, "Please type your comment text.")
		return
	}
	if utf8.RuneCountInString(content) > 65525 {
		commentError(w, http.StatusRequestEntityTooLarge, "Your comment is too long.")
		return
	}
	content = render.SanitizeComment(content)

	ctx := r.Context()
	parentID, _ := strconv.ParseInt(f.Get("comment_parent"), 10, 64)
	if parentID != 0 {
		var n int
		err := s.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE id = ? AND post_id = ? AND status = 'approved'`, parentID, postID).Scan(&n)
		if err != nil || n == 0 {
			parentID = 0
		}
	}
	var dup int
	if err := s.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE post_id = ? AND author = ? AND author_email = ? AND content = ? AND status != 'trash'`,
		postID, author, email, content).Scan(&dup); err == nil && dup > 0 {
		commentError(w, http.StatusConflict, "Duplicate comment detected; it looks as though you’ve already said that!")
		return
	}

	now := time.Now()
	c := &db.Comment{
		PostID: postID, ParentID: parentID, Type: "comment", Author: author, AuthorEmail: email, AuthorURL: website,
		AuthorIP: ip, Agent: truncateRunes(r.UserAgent(), 254), Content: content,
		Date: db.Time{Time: wallClock(now.In(st.s.Loc))}, DateGMT: db.Time{Time: now.UTC().Truncate(time.Second)},
	}
	if vis != nil {
		c.UserID = vis.ID
	}
	c.Status = s.moderate(r, st, c)
	if vis != nil && vis.Can("moderate_comments") {
		c.Status = db.CommentApproved // moderators are trusted (wp_allow_comment)
	}
	if s.commentFilter != nil {
		switch verdict := s.commentFilter(r, c, post); verdict {
		case "discard":
			http.Redirect(w, r, st.links.PostURL(post), http.StatusSeeOther)
			return
		case "":
		default:
			c.Status = verdict
		}
	}
	if err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.InsertComment(ctx, tx, c); err != nil {
			return err
		}
		return db.UpdateCommentCount(ctx, tx, postID)
	}); err != nil {
		s.log.Error("store comment", "err", err)
		commentError(w, http.StatusInternalServerError, "The comment could not be saved. Please try again later.")
		return
	}
	if c.Status == db.CommentApproved {
		s.Invalidate() // only approved comments change public pages
	}
	s.log.Info("comment stored", "id", c.ID, "post", postID, "status", c.Status)
	if s.commentNotify != nil {
		s.commentNotify(ctx, c, false)
	}

	if c.Status != db.CommentSpam && vis == nil {
		remember := !st.s.CookiesOptIn || f.Get("wp-comment-cookies-consent") == "yes"
		s.setCommenterCookies(w, st, author, email, website, remember)
	}
	target := st.links.PostURL(post) + "#comment-" + strconv.FormatInt(c.ID, 10)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// moderate applies the discussion settings (check_comment(),
// wp_check_comment_disallowed_list()).
func (s *Site) moderate(r *http.Request, st *state, c *db.Comment) string {
	set := st.s
	fields := []string{c.Author, c.AuthorEmail, c.AuthorURL, c.Content, c.AuthorIP, c.Agent}
	matches := func(keys []string) bool {
		for _, k := range keys {
			k = strings.ToLower(k)
			for _, f := range fields {
				if strings.Contains(strings.ToLower(f), k) {
					return true
				}
			}
		}
		return false
	}
	if matches(set.DisallowedKeys) {
		return db.CommentSpam
	}
	if set.CommentModeration {
		return db.CommentPending
	}
	if set.CommentMaxLinks > 0 && antispam.CountLinks(c.Content) >= set.CommentMaxLinks {
		return db.CommentPending
	}
	if matches(set.ModerationKeys) {
		return db.CommentPending
	}
	if set.CommentPreviouslyApproved {
		ok, err := db.HasApprovedComment(r.Context(), s.db.R, c.Author, c.AuthorEmail)
		if err != nil || !ok {
			return db.CommentPending
		}
	}
	return db.CommentApproved
}

// setCommenterCookies sets WordPress' commenter cookies (read by
// comment-guard.js to pre-fill the form; only with consent if the consent
// checkbox is enabled) and the signed cookie that reveals the visitor's own
// pending comments.
func (s *Site) setCommenterCookies(w http.ResponseWriter, st *state, author, email, website string, remember bool) {
	h := cookieHash(st.s.Home)
	secure := strings.HasPrefix(st.s.Home, "https://")
	maxAge := 30000000
	if remember {
		for name, value := range map[string]string{"comment_author": author, "comment_author_email": email, "comment_author_url": website} {
			http.SetCookie(w, &http.Cookie{
				Name: name + "_" + h, Value: url.QueryEscape(value), Path: "/",
				MaxAge: maxAge, Secure: secure, SameSite: http.SameSiteLaxMode,
			})
		}
	} else {
		maxAge = 0 // session cookie
	}
	http.SetCookie(w, &http.Cookie{
		Name: commenterCookie, Value: s.signCommenter(email), Path: "/",
		MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// wallClock returns t's wall clock as a UTC time (for *_local columns).
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}
