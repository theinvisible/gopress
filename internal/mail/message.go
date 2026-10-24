package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

var errHeader = errors.New("mail: invalid header value")

// ParseAddress parses a single plain email address ("user@example.org",
// no display name, no line breaks).
func ParseAddress(s string) (string, error) {
	if s == "" || strings.ContainsAny(s, "\r\n<>\",;") {
		return "", errHeader
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", errHeader
	}
	return a.Address, nil
}

// cleanHeader turns a value into a single header line: control
// characters (line breaks above all, which would allow header injection)
// become spaces.
func cleanHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// build returns the complete message (CRLF line endings) for a plain-text
// mail. Addresses must be plain addresses; the sender name and subject
// are cleaned and MIME-encoded.
func build(fromName, from, to, subject, body string, now time.Time) ([]byte, error) {
	from, err := ParseAddress(from)
	if err != nil {
		return nil, err
	}
	if to, err = ParseAddress(to); err != nil {
		return nil, err
	}
	domain := from[strings.LastIndexByte(from, '@')+1:]
	id := make([]byte, 16)
	rand.Read(id)

	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", (&mail.Address{Name: cleanHeader(fromName), Address: from}).String())
	header("To", (&mail.Address{Address: to}).String())
	header("Subject", mime.QEncoding.Encode("utf-8", cleanHeader(subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	header("X-Auto-Response-Suppress", "All")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\r\n", "\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
