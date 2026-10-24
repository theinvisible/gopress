package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/config"
	"github.com/theinvisible/gopress/internal/db"
)

const userUsage = `usage: gopress user <command> [flags]

Break-glass user management for whoever has access to the server.

commands:
  list                           list users
  add --login L --email E [--role R] [--display-name N]
                                 create a user (asks for the password)
  set-password LOGIN             set a new password (asks for it) and log out all sessions
  reset-2fa LOGIN                remove all second factors and sessions; the user sets
                                 up two-factor authentication again at the next login
  revoke-sessions LOGIN          log the user out everywhere
`

func cmdUser(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, userUsage) }
	cfg.RegisterFlags(fs)
	login := fs.String("login", "", "login name (add)")
	email := fs.String("email", "", "email address (add)")
	role := fs.String("role", auth.RoleAdministrator, "role (add): administrator, editor, author, contributor, subscriber")
	display := fs.String("display-name", "", "display name (add)")
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	sub := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "gopress.db"))
	if err != nil {
		fmt.Fprintf(stderr, "user: %v\n", err)
		return 1
	}
	defer d.Close()

	target := func() (*db.User, bool) {
		if fs.NArg() != 1 {
			fs.Usage()
			return nil, false
		}
		u, err := db.UserByLogin(ctx, d.R, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "user: no user %q\n", fs.Arg(0))
			return nil, false
		}
		return u, true
	}
	audit := func(userID int64, action string) {
		db.InsertAudit(ctx, d.W, &db.AuditEntry{Time: time.Now().Unix(), UserID: userID, IP: "cli", Action: action,
			ObjectType: "user", ObjectID: userID})
	}

	switch sub {
	case "list":
		us, err := db.Users(ctx, d.R)
		if err != nil {
			fmt.Fprintf(stderr, "user: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%-4s %-20s %-30s %-14s %s\n", "ID", "LOGIN", "EMAIL", "ROLE", "2FA")
		for _, u := range us {
			fmt.Fprintf(stdout, "%-4d %-20s %-30s %-14s %s\n", u.ID, u.Login, u.Email, u.Role, factors(ctx, d, u.ID))
		}
		return 0

	case "add":
		if *login == "" || *email == "" {
			fmt.Fprintln(stderr, "user add: --login and --email are required")
			return 2
		}
		if _, ok := map[string]bool{auth.RoleAdministrator: true, auth.RoleEditor: true, auth.RoleAuthor: true,
			auth.RoleContributor: true, auth.RoleSubscriber: true}[*role]; !ok {
			fmt.Fprintf(stderr, "user add: unknown role %q\n", *role)
			return 2
		}
		if _, err := db.UserByLogin(ctx, d.R, *login); !errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(stderr, "user add: login %q exists\n", *login)
			return 1
		}
		hash, ok := askPassword(ctx, stdin, stderr, *login, *email)
		if !ok {
			return 1
		}
		name := *display
		if name == "" {
			name = *login
		}
		u := &db.User{Login: *login, Nicename: strings.ToLower(*login), Email: *email, DisplayName: name,
			PassHash: hash, Role: *role, RegisteredGMT: db.Time{Time: time.Now().UTC()}}
		if err := db.InsertUser(ctx, d.W, u); err != nil {
			fmt.Fprintf(stderr, "user add: %v\n", err)
			return 1
		}
		audit(u.ID, "user_created")
		fmt.Fprintf(stdout, "created user %s (id %d); two-factor authentication is set up at the first login\n", u.Login, u.ID)
		return 0

	case "set-password":
		u, ok := target()
		if !ok {
			return 2
		}
		hash, ok := askPassword(ctx, stdin, stderr, u.Login, u.Email)
		if !ok {
			return 1
		}
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if err := db.SetUserPassHash(ctx, tx, u.ID, hash); err != nil {
				return err
			}
			return db.DeleteUserSessions(ctx, tx, u.ID, nil)
		})
		if err != nil {
			fmt.Fprintf(stderr, "user: %v\n", err)
			return 1
		}
		audit(u.ID, auth.ActPasswordChange)
		fmt.Fprintf(stdout, "password of %s changed, all sessions ended\n", u.Login)
		return 0

	case "reset-2fa":
		u, ok := target()
		if !ok {
			return 2
		}
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if err := db.DeleteSecondFactors(ctx, tx, u.ID); err != nil {
				return err
			}
			return db.DeleteUserSessions(ctx, tx, u.ID, nil)
		})
		if err != nil {
			fmt.Fprintf(stderr, "user: %v\n", err)
			return 1
		}
		audit(u.ID, "2fa_reset")
		fmt.Fprintf(stdout, "second factors of %s removed, all sessions ended\n", u.Login)
		return 0

	case "revoke-sessions":
		u, ok := target()
		if !ok {
			return 2
		}
		if err := db.DeleteUserSessions(ctx, d.W, u.ID, nil); err != nil {
			fmt.Fprintf(stderr, "user: %v\n", err)
			return 1
		}
		audit(u.ID, auth.ActSessionsRevoke)
		fmt.Fprintf(stdout, "all sessions of %s ended\n", u.Login)
		return 0
	}
	fs.Usage()
	return 2
}

func factors(ctx context.Context, d *db.DB, userID int64) string {
	var out []string
	if _, _, ok, _ := db.TOTP(ctx, d.R, userID); ok {
		out = append(out, "totp")
	}
	if creds, _ := db.WebAuthnCredentials(ctx, d.R, userID); len(creds) > 0 {
		out = append(out, fmt.Sprintf("passkeys:%d", len(creds)))
	}
	if n, _ := db.RecoveryCodesLeft(ctx, d.R, userID); n > 0 {
		out = append(out, fmt.Sprintf("recovery:%d", n))
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

// askPassword reads a new password (twice on a terminal, once from a pipe),
// checks the policy and returns its hash. Passwords are never taken from
// flags, which would leak them into the shell history and process list.
func askPassword(ctx context.Context, stdin io.Reader, stderr io.Writer, login, email string) (string, bool) {
	read := func(prompt string) (string, error) {
		if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			fmt.Fprint(stderr, prompt)
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(stderr)
			return string(b), err
		}
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := read("New password: ")
	if err != nil {
		fmt.Fprintf(stderr, "user: %v\n", err)
		return "", false
	}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		again, err := read("Repeat password: ")
		if err != nil || again != pw {
			fmt.Fprintln(stderr, "user: passwords do not match")
			return "", false
		}
	}
	if err := auth.CheckPasswordPolicy(pw, login, email); err != nil {
		fmt.Fprintf(stderr, "user: %v\n", err)
		return "", false
	}
	hash, err := auth.HashPassword(ctx, pw)
	if err != nil {
		fmt.Fprintf(stderr, "user: %v\n", err)
		return "", false
	}
	return hash, true
}
