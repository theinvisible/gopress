package content

import (
	"context"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
)

// Mailer queues email (internal/mail).
type Mailer interface {
	Enqueue(ctx context.Context, to, template string, data mail.Data) error
}

// CommentStatusHook is told about comments whose status an editor
// changed (approve, unapprove, spam, unspam, trash, untrash): their ids,
// each one's status before, the action and the editor.
type CommentStatusHook func(ctx context.Context, ids []int64, previous []string, action string, by *db.User)

// SetCommentStatusHook installs the comment status hook; the plugin host
// uses it.
func (s *Service) SetCommentStatusHook(f CommentStatusHook) {
	s.statusHook = f
}

// SetMailer enables email notifications.
func (s *Service) SetMailer(m Mailer) { s.mail = m }

// NotifyComment sends the notifications WordPress sends for a comment
// that was just stored (or approved, when approved is true):
// moderation_notify mails pending comments to the admin email address and
// to the post author if they may moderate; comments_notify mails approved
// comments to the post author unless they wrote the comment themselves.
// Errors are logged: a notification must never fail the comment.
func (s *Service) NotifyComment(ctx context.Context, c *db.Comment, approved bool) {
	if s.mail == nil || c.Status == db.CommentSpam || c.Status == db.CommentTrash {
		return
	}
	st, err := s.site.Settings(ctx)
	if err != nil {
		s.log.Error("comment notification", "err", err)
		return
	}
	post, err := db.GetPost(ctx, s.db.R, c.PostID)
	if err != nil {
		return
	}
	author, _ := db.GetUser(ctx, s.db.R, post.AuthorID)
	text := c.Content
	if len(text) > 4000 {
		text = text[:4000] + " …"
	}
	data := mail.Data{
		"PostTitle": oneLine(post.Title), "Author": oneLine(c.Author), "AuthorEmail": c.AuthorEmail,
		"AuthorURL": c.AuthorURL, "IP": c.AuthorIP, "Text": text,
		"Link": st.Home + "/?p=" + strconv.FormatInt(post.ID, 10) + "#comment-" + strconv.FormatInt(c.ID, 10),
	}
	send := func(to, tpl string) {
		if to == "" {
			return
		}
		if err := s.mail.Enqueue(ctx, to, tpl, data); err != nil {
			s.log.Warn("comment notification", "to", to, "err", err)
		}
	}
	switch {
	case c.Status == db.CommentPending && !approved && st.ModerationNotify:
		counts, _ := db.CommentStatusCounts(ctx, s.db.R, 0)
		data["Pending"] = counts[db.CommentPending]
		send(st.AdminEmail, "comment_moderation")
		if author != nil && auth.Can(author, auth.CapModerateComments) && !strings.EqualFold(author.Email, st.AdminEmail) {
			send(author.Email, "comment_moderation")
		}
	case c.Status == db.CommentApproved && st.CommentsNotify && author != nil:
		if c.UserID == author.ID || strings.EqualFold(c.AuthorEmail, author.Email) {
			return // own comment
		}
		send(author.Email, "comment_notify")
	}
}

// oneLine collapses whitespace (titles and names go into the subject).
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
