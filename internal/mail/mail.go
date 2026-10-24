// Package mail sends the site's email: password resets, invitations,
// security notices and comment notifications. Messages are rendered from
// plain-text templates and put into a queue table; a background worker
// delivers them over SMTP with mandatory TLS. Requests therefore never
// wait for the mail server, and their timing does not reveal whether a
// message was sent (e.g. for an unknown account).
package mail

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

//go:embed templates/*.txt
var templateFS embed.FS

var templates = template.Must(template.New("").Option("missingkey=zero").ParseFS(templateFS, "templates/*.txt"))

// OptionName is the option holding the SMTP configuration.
const OptionName = "mail_smtp"

// Options configures the mail service.
type Options struct {
	Secret   []byte
	SiteName func() string
	Home     func() string
	// Sink, if set, is a directory that receives every message as an .eml
	// file instead of sending it (development and end-to-end tests).
	Sink string
	Log  *slog.Logger
}

// Service queues and delivers mail.
type Service struct {
	db   *db.DB
	opts Options
	key  []byte
	now  func() time.Time
	wake chan struct{}
	// send delivers one message; replaced in tests.
	send func(ctx context.Context, c *Config, password, to string, msg []byte) error
}

// New creates the service.
func New(d *db.DB, opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.SiteName == nil {
		opts.SiteName = func() string { return "GoPress" }
	}
	if opts.Home == nil {
		opts.Home = func() string { return "" }
	}
	key, err := hkdf.Key(sha256.New, opts.Secret, nil, "gopress smtp password", 32)
	if err != nil {
		panic(err)
	}
	s := &Service{db: d, opts: opts, key: key, now: time.Now, wake: make(chan struct{}, 1)}
	s.send = func(ctx context.Context, c *Config, password, to string, msg []byte) error {
		return sendSMTP(ctx, c, password, s.heloName(), to, msg)
	}
	return s
}

func (s *Service) heloName() string {
	if u, err := url.Parse(s.opts.Home()); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// Config returns the stored SMTP configuration (nil if none).
func (s *Service) Config(ctx context.Context) (*Config, error) {
	var c Config
	ok, err := db.GetOption(ctx, s.db.R, OptionName, &c)
	if err != nil || !ok {
		return nil, err
	}
	return &c, nil
}

// Ready reports whether mail can be delivered (configured or sink).
func (s *Service) Ready(ctx context.Context) bool {
	if s.opts.Sink != "" {
		return true
	}
	c, err := s.Config(ctx)
	return err == nil && c != nil && c.Host != ""
}

// SaveConfig validates and stores c. password replaces the stored one
// when keep is false; an empty password with keep false removes it.
func (s *Service) SaveConfig(ctx context.Context, c *Config, password string, keep bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if keep {
		if old, err := s.Config(ctx); err == nil && old != nil && old.PasswordEnc != "" {
			// The stored password only goes to the server it was entered
			// for: a changed server needs it again.
			if old.Host != c.Host || old.Port != c.Port || old.Security != c.Security || old.User != c.User {
				return errors.New("Please enter the password again when you change the server, port, security or username.")
			}
			c.PasswordEnc = old.PasswordEnc
		}
	} else {
		c.PasswordEnc = ""
		if password != "" {
			enc, err := s.seal([]byte(password))
			if err != nil {
				return err
			}
			c.PasswordEnc = enc
		}
	}
	return db.SetOption(ctx, s.db.W, OptionName, c)
}

// DeleteConfig removes the SMTP configuration.
func (s *Service) DeleteConfig(ctx context.Context) error {
	_, err := s.db.W.ExecContext(ctx, `DELETE FROM options WHERE name = ?`, OptionName)
	return err
}

func (s *Service) seal(plain []byte) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, []byte(OptionName))), nil
}

func (s *Service) password(c *Config) (string, error) {
	if c.PasswordEnc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(c.PasswordEnc)
	if err != nil {
		return "", err
	}
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("mail: stored password is corrupt")
	}
	p, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(OptionName))
	if err != nil {
		return "", errors.New("mail: stored password cannot be decrypted (was secret.key replaced?)")
	}
	return string(p), nil
}

func (s *Service) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Data is the template data of a message; SiteName and Home are added.
type Data map[string]any

// render executes template name; its first line is "Subject: …".
func (s *Service) render(name string, data Data) (subject, body string, err error) {
	d := Data{"SiteName": s.opts.SiteName(), "Home": strings.TrimRight(s.opts.Home(), "/")}
	for k, v := range data {
		d[k] = v
	}
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, name+".txt", d); err != nil {
		return "", "", err
	}
	first, rest, _ := strings.Cut(b.String(), "\n")
	subject, ok := strings.CutPrefix(first, "Subject: ")
	if !ok {
		return "", "", fmt.Errorf("mail: template %s has no subject", name)
	}
	body = strings.TrimSpace(rest) + "\n\n-- \n" + d["SiteName"].(string) + "\n" + d["Home"].(string) + "\n"
	return cleanHeader(subject), body, nil
}

// Enqueue renders template name for to and queues it. Invalid addresses
// are an error; delivery problems are not (they are retried).
func (s *Service) Enqueue(ctx context.Context, to, name string, data Data) error {
	return s.EnqueueUntil(ctx, to, name, data, time.Time{})
}

// EnqueueUntil queues a message with a one-time link that expires at
// expires: afterwards it is neither sent nor kept.
func (s *Service) EnqueueUntil(ctx context.Context, to, name string, data Data, expires time.Time) error {
	to, err := ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return err
	}
	subject, body, err := s.render(name, data)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	var exp int64
	if !expires.IsZero() {
		exp = expires.Unix()
	}
	if err := db.EnqueueMail(ctx, s.db.W, &db.QueuedMail{To: to, Subject: subject, Body: body, Created: now, NextTry: now, Expires: exp}); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// Wake makes the worker look at the queue now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// SendTest sends a test message to to right away and returns the error
// of the mail server, if any.
func (s *Service) SendTest(ctx context.Context, to string) error {
	subject, body, err := s.render("test", nil)
	if err != nil {
		return err
	}
	return s.deliver(ctx, &db.QueuedMail{To: to, Subject: subject, Body: body})
}

// retryDelays is the backoff between delivery attempts; after the last
// one the message is marked failed.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 6 * time.Hour}

const keepMail = 7 * 24 * time.Hour

// Run delivers queued mail until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.Flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// Flush delivers the messages that are due now.
func (s *Service) Flush(ctx context.Context) {
	now := s.now()
	if err := db.PruneMail(ctx, s.db.W, now.Add(-keepMail).Unix(), now.Unix()); err != nil {
		s.opts.Log.Error("prune mail", "err", err)
	}
	if !s.Ready(ctx) {
		return // stays queued until mail is configured
	}
	due, err := db.DueMail(ctx, s.db.R, now.Unix(), 20)
	if err != nil {
		s.opts.Log.Error("mail queue", "err", err)
		return
	}
	for i := range due {
		m := &due[i]
		err := s.deliver(ctx, m)
		if err == nil {
			s.opts.Log.Info("mail sent", "id", m.ID, "subject", m.Subject)
			if err := db.DeleteMail(ctx, s.db.W, m.ID); err != nil {
				s.opts.Log.Error("mail queue", "err", err)
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		var next int64
		if m.Tries < len(retryDelays) {
			next = s.now().Add(retryDelays[m.Tries]).Unix()
		}
		s.opts.Log.Warn("mail delivery failed", "id", m.ID, "try", m.Tries+1, "err", err)
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if err := db.MailAttemptFailed(ctx, s.db.W, m.ID, next, msg); err != nil {
			s.opts.Log.Error("mail queue", "err", err)
		}
	}
}

// deliver builds and sends one message (or writes it to the sink).
func (s *Service) deliver(ctx context.Context, m *db.QueuedMail) error {
	if s.opts.Sink != "" {
		msg, err := build(s.opts.SiteName(), "gopress@localhost", m.To, m.Subject, m.Body, s.now())
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%d-%d.eml", s.now().UnixNano(), m.ID)
		return os.WriteFile(filepath.Join(s.opts.Sink, name), msg, 0o600)
	}
	c, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("email is not configured")
	}
	name := c.FromName
	if name == "" {
		name = s.opts.SiteName()
	}
	msg, err := build(name, c.From, m.To, m.Subject, m.Body, s.now())
	if err != nil {
		return err
	}
	pw, err := s.password(c)
	if err != nil {
		return err
	}
	return s.send(ctx, c, pw, m.To, msg)
}
