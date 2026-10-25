package mail

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

func newTestService(t *testing.T) (*Service, *db.DB) {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "gopress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s := New(d, Options{Secret: []byte("0123456789abcdef0123456789abcdef"), SiteName: func() string { return "Test Site" },
		Home: func() string { return "https://example.org" }})
	return s, d
}

func TestBuildMessage(t *testing.T) {
	msg, err := build("Site\r\nBcc: evil@example.org", "site@example.org", "user@example.org",
		"Hello\r\nBcc: evil@example.org Ünïcödé", "Line one\nLine two ünïcödé "+strings.Repeat("x", 100)+"\n", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(msg)))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Header["Bcc"]) != 0 {
		t.Error("header injection through name or subject")
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subj != "Hello Bcc: evil@example.org Ünïcödé" {
		t.Errorf("subject = %q", subj)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
	if !strings.Contains(string(body), "Line two ünïcödé") {
		t.Errorf("body = %q", body)
	}
	for _, line := range strings.Split(string(msg), "\r\n") {
		if len(line) > 78 {
			t.Errorf("line longer than 78 characters: %q", line)
		}
	}
	for _, bad := range []string{"a@b.org\r\nBcc: c@d.org", "Name <a@b.org>", "a@b.org, c@d.org", "", "nobody"} {
		if _, err := build("", "site@example.org", bad, "s", "b", time.Now()); err == nil {
			t.Errorf("recipient %q accepted", bad)
		}
	}
}

func TestConfig(t *testing.T) {
	s, d := newTestService(t)
	ctx := context.Background()
	for _, c := range []Config{
		{Host: "mail.example.org", Port: 587, Security: SecurityNone, From: "a@example.org"},
		{Host: "mail.example.org", Port: 0, Security: SecurityStartTLS, From: "a@example.org"},
		{Host: "mail.example.org", Port: 587, Security: SecurityStartTLS, From: "not an address"},
		{Host: "", Port: 587, Security: SecurityTLS, From: "a@example.org"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("invalid config accepted: %+v", c)
		}
	}
	c := &Config{Host: "127.0.0.1", Port: 25, Security: SecurityNone, User: "u", From: "site@example.org"}
	if err := s.SaveConfig(ctx, c, "s3cret password", false); err != nil {
		t.Fatal(err)
	}
	var raw string
	d.R.QueryRow(`SELECT value FROM options WHERE name = ?`, OptionName).Scan(&raw)
	if strings.Contains(raw, "s3cret") {
		t.Error("password stored in plain text")
	}
	got, _ := s.Config(ctx)
	if pw, err := s.password(got); pw != "s3cret password" || err != nil {
		t.Errorf("password = %q, %v", pw, err)
	}
	// Keeping the password on save (same server).
	got.FromName = "Site"
	if err := s.SaveConfig(ctx, got, "", true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Config(ctx)
	if pw, _ := s.password(got); pw != "s3cret password" || got.FromName != "Site" {
		t.Errorf("password not kept: %q", pw)
	}
	// Review fix: the stored password does not follow a new server.
	moved := *got
	moved.Host = "attacker.example"
	if err := s.SaveConfig(ctx, &moved, "", true); err == nil {
		t.Error("stored password kept for another server")
	}
	// Another secret cannot decrypt it.
	other := New(d, Options{Secret: []byte("another secret of thirty-two by!")})
	if _, err := other.password(got); err == nil {
		t.Error("password decrypted with another secret")
	}
}

func TestQueue(t *testing.T) {
	s, d := newTestService(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }

	if err := s.Enqueue(ctx, "user@example.org\r\nBcc: x@y.org", "test", nil); err == nil {
		t.Error("invalid recipient queued")
	}
	if err := s.Enqueue(ctx, "user@example.org", "password_reset", Data{"Login": "user", "Link": "https://example.org/x", "Valid": "1 hour"}); err != nil {
		t.Fatal(err)
	}
	// Not configured: the message waits.
	s.Flush(ctx)
	if q, _ := db.MailQueue(ctx, d.R, 10); len(q) != 1 || q[0].Tries != 0 {
		t.Fatalf("queue = %+v", q)
	}
	s.SaveConfig(ctx, &Config{Host: "mail.example.org", Port: 587, Security: SecurityStartTLS, From: "site@example.org"}, "", false)

	var sent []string
	fail := true
	s.send = func(_ context.Context, c *Config, pw, to string, msg []byte) error {
		if fail {
			return errors.New("connection refused")
		}
		sent = append(sent, string(msg))
		return nil
	}
	s.Flush(ctx)
	q, _ := db.MailQueue(ctx, d.R, 10)
	if len(q) != 1 || q[0].Tries != 1 || q[0].NextTry != now.Add(time.Minute).Unix() || q[0].LastError != "connection refused" {
		t.Fatalf("after failure: %+v", q)
	}
	s.Flush(ctx) // not due yet
	if q, _ := db.MailQueue(ctx, d.R, 10); q[0].Tries != 1 {
		t.Error("retried before the backoff")
	}
	// All retries fail: marked failed.
	for range retryDelays {
		now = now.Add(7 * time.Hour)
		s.Flush(ctx)
	}
	if q, _ := db.MailQueue(ctx, d.R, 10); q[0].Status != db.MailFailed {
		t.Errorf("status = %q, want failed", q[0].Status)
	}
	db.RetryMail(ctx, d.W, q[0].ID, now.Unix())
	fail = false
	s.Flush(ctx)
	if q, _ := db.MailQueue(ctx, d.R, 10); len(q) != 0 || len(sent) != 1 {
		t.Fatalf("not delivered: queue %d, sent %d", len(q), len(sent))
	}
	if !strings.Contains(sent[0], "Subject: [Test Site] Password reset") || !strings.Contains(sent[0], "From: \"Test Site\" <site@example.org>") {
		t.Errorf("message:\n%s", sent[0])
	}
	// Old messages are pruned.
	s.Enqueue(ctx, "user@example.org", "test", nil)
	fail = true
	now = now.Add(keepMail + time.Hour)
	s.Flush(ctx)
	if q, _ := db.MailQueue(ctx, d.R, 10); len(q) != 0 {
		t.Error("old message not pruned")
	}
}

func TestSink(t *testing.T) {
	s, _ := newTestService(t)
	s.opts.Sink = t.TempDir()
	ctx := context.Background()
	s.Enqueue(ctx, "user@example.org", "security", Data{"Login": "user", "Event": "Password changed", "IP": "192.0.2.1"})
	s.Flush(ctx)
	files, _ := filepath.Glob(filepath.Join(s.opts.Sink, "*.eml"))
	if len(files) != 1 {
		t.Fatalf("sink files = %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "Password changed") || !strings.Contains(string(b), "https://example.org/admin/profile/security") {
		t.Errorf("message:\n%s", b)
	}
}

func TestTemplates(t *testing.T) {
	s, _ := newTestService(t)
	for _, tpl := range templates.Templates() {
		name := strings.TrimSuffix(tpl.Name(), ".txt")
		if name == "" {
			continue
		}
		subject, body, err := s.render(name, Data{})
		if err != nil || subject == "" || !strings.HasSuffix(body, "Test Site\nhttps://example.org\n") {
			t.Errorf("%s: %q, %v", name, subject, err)
		}
	}
}

// Review fix: messages with expired one-time links are neither sent nor
// kept in the queue.
func TestExpiredLinkMail(t *testing.T) {
	s, d := newTestService(t)
	ctx := context.Background()
	s.opts.Sink = t.TempDir()
	s.EnqueueUntil(ctx, "user@example.org", "password_reset", Data{"Link": "https://example.org/login/reset/x"}, time.Now().Add(-time.Minute))
	s.EnqueueUntil(ctx, "user@example.org", "invitation", Data{"Link": "https://example.org/login/invite/y"}, time.Now().Add(time.Hour))
	s.Flush(ctx)
	files, _ := filepath.Glob(filepath.Join(s.opts.Sink, "*.eml"))
	if len(files) != 1 {
		t.Fatalf("sent %d messages, want 1", len(files))
	}
	if q, _ := db.MailQueue(ctx, d.R, 10); len(q) != 0 {
		t.Errorf("queue keeps %d messages", len(q))
	}
}

func TestDisabled(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "gopress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, Options{Secret: []byte("0123456789abcdef0123456789abcdef"), SiteName: func() string { return "Test Site" },
		Home: func() string { return "https://example.org" }, Disabled: true})
	s.SaveConfig(ctx, &Config{Host: "mail.example.org", Port: 587, Security: SecurityStartTLS, From: "site@example.org"}, "", false)
	s.send = func(context.Context, *Config, string, string, []byte) error {
		t.Error("mail sent although disabled")
		return nil
	}
	if s.Ready(ctx) {
		t.Error("ready although disabled")
	}
	if err := s.SendTest(ctx, "admin@example.org"); !errors.Is(err, ErrDisabled) {
		t.Errorf("test mail: %v", err)
	}
	if err := s.Enqueue(ctx, "user@example.org", "test", nil); err != nil {
		t.Fatal(err)
	}
	s.Flush(ctx)
	// Not kept for later: a restart with sending enabled must not mail it.
	if q, _ := db.MailQueue(ctx, d.R, 10); len(q) != 1 || q[0].Status != db.MailFailed {
		t.Errorf("queue = %+v", q)
	}
}
