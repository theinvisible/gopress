package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// Transport security of the SMTP connection.
const (
	SecurityStartTLS = "starttls" // submission port 587: STARTTLS required
	SecurityTLS      = "tls"      // port 465: implicit TLS
	SecurityNone     = "none"     // only to a relay on the loopback interface
)

// Config is the SMTP configuration, stored in the option mail_smtp. The
// password is encrypted with a key derived from the site secret and never
// leaves the server.
type Config struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	User        string `json:"user,omitempty"`
	PasswordEnc string `json:"password_enc,omitempty"`
	From        string `json:"from"`
	FromName    string `json:"from_name,omitempty"`
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	if c.Host == "" || len(c.Host) > 253 {
		return errors.New("Please enter the SMTP server.")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("Please enter a valid port.")
	}
	switch c.Security {
	case SecurityStartTLS, SecurityTLS:
	case SecurityNone:
		if !isLoopback(c.Host) {
			return errors.New("Unencrypted connections are only allowed to a mail relay on this machine (localhost).")
		}
	default:
		return errors.New("Please choose the connection security.")
	}
	if _, err := ParseAddress(c.From); err != nil {
		return errors.New("Please enter a valid sender address.")
	}
	if len(c.User) > 254 || len(c.FromName) > 100 {
		return errors.New("A field is too long.")
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const smtpTimeout = 60 * time.Second

// sendSMTP delivers one message. TLS (implicit or STARTTLS) is mandatory
// except for a loopback relay, certificates are verified and the password
// is only sent over TLS (net/smtp's PlainAuth enforces that as well).
func sendSMTP(ctx context.Context, c *Config, password, heloName, to string, msg []byte) error {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(smtpTimeout))
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.Security == SecurityTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return err
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if err := cl.Hello(heloName); err != nil {
		return err
	}
	if c.Security == SecurityStartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if c.User != "" {
		if ok, _ := cl.Extension("AUTH"); !ok {
			return errors.New("the server does not offer authentication")
		}
		if err := cl.Auth(smtp.PlainAuth("", c.User, password, c.Host)); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	wc, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
