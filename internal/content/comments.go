package content

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// Comment moderation actions.
const (
	CommentApprove   = "approve"
	CommentUnapprove = "unapprove"
	CommentSpam      = "spam"
	CommentUnspam    = "unspam"
	CommentTrash     = "trash"
	CommentUntrash   = "untrash"
	CommentDelete    = "delete"
)

// ModerateComments applies action to the comments with ids and returns how
// many changed.
func (e *Editor) ModerateComments(ctx context.Context, action string, ids []int64) (int, error) {
	if !auth.Can(e.user, auth.CapModerateComments) {
		return 0, ErrForbidden
	}
	posts := map[int64]bool{}
	var approved []*db.Comment // pending → approved: the post author is notified
	n := 0
	err := e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		approved = approved[:0]
		for _, id := range ids {
			c, err := db.GetComment(ctx, tx, id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			switch action {
			case CommentApprove:
				err = db.SetCommentStatus(ctx, tx, id, db.CommentApproved)
				if c.Status == db.CommentPending {
					approved = append(approved, c)
				}
			case CommentUnapprove:
				err = db.SetCommentStatus(ctx, tx, id, db.CommentPending)
			case CommentSpam:
				err = db.SetCommentStatus(ctx, tx, id, db.CommentSpam)
			case CommentTrash:
				err = db.SetCommentStatus(ctx, tx, id, db.CommentTrash)
			case CommentUnspam, CommentUntrash:
				if (action == CommentUnspam) != (c.Status == db.CommentSpam) {
					continue
				}
				err = db.SetCommentStatus(ctx, tx, id, "")
			case CommentDelete:
				if c.Status != db.CommentSpam && c.Status != db.CommentTrash {
					return &ValidationError{"Only spam and trashed comments can be deleted permanently."}
				}
				err = db.DeleteComment(ctx, tx, id)
			default:
				return ErrInvalid
			}
			if err != nil {
				return err
			}
			posts[c.PostID] = true
			n++
		}
		for id := range posts {
			if err := db.UpdateCommentCount(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		e.s.audit(ctx, e.r, e.user.ID, "comments_"+action, "comment", 0, map[string]any{"ids": ids})
		e.s.site.Invalidate()
	}
	for _, c := range approved {
		c.Status = db.CommentApproved
		e.s.NotifyComment(ctx, c, true)
	}
	return n, nil
}

// EmptyComments deletes all comments with status spam or trash.
func (e *Editor) EmptyComments(ctx context.Context, status string) (int, error) {
	if status != db.CommentSpam && status != db.CommentTrash {
		return 0, ErrInvalid
	}
	ids, err := db.CommentIDsWithStatus(ctx, e.s.db.R, status)
	if err != nil {
		return 0, err
	}
	return e.ModerateComments(ctx, CommentDelete, ids)
}

// ReplyComment adds an approved reply by the current user (also approving
// the comment replied to, like WordPress does).
func (e *Editor) ReplyComment(ctx context.Context, parent *db.Comment, text string) (*db.Comment, error) {
	if !auth.Can(e.user, auth.CapModerateComments) {
		return nil, ErrForbidden
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, &ValidationError{"Please type your reply."}
	}
	if len(text) > 65525 {
		return nil, &ValidationError{"Your reply is too long."}
	}
	if !auth.Can(e.user, auth.CapUnfilteredHTML) {
		text = render.SanitizeComment(text)
	}
	settings, err := e.s.site.Settings(ctx)
	if err != nil {
		return nil, err
	}
	loc := settings.Loc
	if loc == nil {
		loc = time.UTC
	}
	now := e.s.now()
	c := &db.Comment{PostID: parent.PostID, ParentID: parent.ID, Type: "comment", Status: db.CommentApproved,
		Author: e.user.DisplayName, AuthorEmail: e.user.Email, AuthorURL: e.user.URL, UserID: e.user.ID,
		AuthorIP: clientIP(e), Agent: truncate(userAgent(e), 255),
		Date: db.Time{Time: wall(now.In(loc))}, DateGMT: db.Time{Time: now.UTC()}, Content: text}
	err = e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		if parent.ID != 0 && parent.Status == db.CommentPending {
			if err := db.SetCommentStatus(ctx, tx, parent.ID, db.CommentApproved); err != nil {
				return err
			}
		}
		if err := db.InsertComment(ctx, tx, c); err != nil {
			return err
		}
		return db.UpdateCommentCount(ctx, tx, c.PostID)
	})
	if err != nil {
		return nil, err
	}
	e.s.audit(ctx, e.r, e.user.ID, "comment_reply", "comment", c.ID, nil)
	e.s.site.Invalidate()
	e.s.NotifyComment(ctx, c, false)
	return c, nil
}

// CommentInput holds the editable fields of a comment.
type CommentInput struct {
	Author, Email, URL, Content, Status string
	Date                                string // local "YYYY-MM-DDTHH:MM"; empty keeps it
}

// UpdateComment edits a comment.
func (e *Editor) UpdateComment(ctx context.Context, c *db.Comment, in *CommentInput) error {
	if !auth.Can(e.user, auth.CapModerateComments) {
		return ErrForbidden
	}
	in.Author, in.Email, in.URL = strings.TrimSpace(in.Author), strings.TrimSpace(in.Email), strings.TrimSpace(in.URL)
	if in.Email != "" {
		if _, err := mail.ParseAddress(in.Email); err != nil {
			return &ValidationError{"Please enter a valid email address."}
		}
	}
	if in.URL != "" {
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return &ValidationError{"The website must be an http(s) URL."}
		}
	}
	if strings.TrimSpace(in.Content) == "" {
		return &ValidationError{"The comment text cannot be empty."}
	}
	switch in.Status {
	case db.CommentApproved, db.CommentPending, db.CommentSpam:
	default:
		return &ValidationError{"Unknown comment status."}
	}
	cp := *c
	cp.Author, cp.AuthorEmail, cp.AuthorURL, cp.Status = render.SanitizeText(in.Author), in.Email, in.URL, in.Status
	cp.Content = in.Content
	if !auth.Can(e.user, auth.CapUnfilteredHTML) {
		cp.Content = render.SanitizeComment(cp.Content)
	}
	if in.Date != "" {
		settings, err := e.s.site.Settings(ctx)
		if err != nil {
			return err
		}
		loc := settings.Loc
		if loc == nil {
			loc = time.UTC
		}
		t, err := time.ParseInLocation("2006-01-02T15:04", in.Date, loc)
		if err != nil {
			return &ValidationError{"Invalid date."}
		}
		cp.Date, cp.DateGMT = db.Time{Time: wall(t)}, db.Time{Time: t.UTC()}
	}
	err := e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.UpdateComment(ctx, tx, &cp); err != nil {
			return err
		}
		return db.UpdateCommentCount(ctx, tx, cp.PostID)
	})
	if err != nil {
		return err
	}
	e.s.audit(ctx, e.r, e.user.ID, "comment_updated", "comment", c.ID, nil)
	e.s.site.Invalidate()
	return nil
}

func clientIP(e *Editor) string {
	if e.r == nil || e.s.auth == nil {
		return ""
	}
	return e.s.auth.ClientIP(e.r)
}

func userAgent(e *Editor) string {
	if e.r == nil {
		return ""
	}
	return e.r.UserAgent()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
