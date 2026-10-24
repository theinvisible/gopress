package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"path/filepath"
	"time"

	"github.com/theinvisible/gopress/internal/config"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
	"github.com/theinvisible/gopress/internal/site"
)

const mailUsage = `usage: gopress mail <command> [flags]

commands:
  queue                  list queued and failed messages
  test --to ADDRESS      send a test email right away (uses the SMTP settings
                         from Settings > Email)
`

func cmdMail(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("mail", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, mailUsage) }
	cfg.RegisterFlags(fs)
	to := fs.String("to", "", "recipient (test)")
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
		fmt.Fprintf(stderr, "mail: %v\n", err)
		return 1
	}
	defer d.Close()

	switch sub {
	case "queue":
		q, err := db.MailQueue(ctx, d.R, 200)
		if err != nil {
			fmt.Fprintf(stderr, "mail: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%-6s %-17s %-8s %-5s %-30s %s\n", "ID", "QUEUED", "STATUS", "TRIES", "TO", "SUBJECT")
		for _, m := range q {
			fmt.Fprintf(stdout, "%-6d %-17s %-8s %-5d %-30s %s\n", m.ID, time.Unix(m.Created, 0).Format("2006-01-02 15:04"),
				m.Status, m.Tries, m.To, m.Subject)
			if m.LastError != "" {
				fmt.Fprintf(stdout, "       last error: %s\n", m.LastError)
			}
		}
		return 0
	case "test":
		if *to == "" {
			fmt.Fprintln(stderr, "mail test: --to is required")
			return 2
		}
		secret, err := config.LoadOrCreateSecret(filepath.Join(cfg.DataDir, "secret.key"))
		if err != nil {
			fmt.Fprintf(stderr, "mail: %v\n", err)
			return 1
		}
		st, err := site.LoadSettings(ctx, d.R)
		if err != nil {
			fmt.Fprintf(stderr, "mail: %v\n", err)
			return 1
		}
		svc := mail.New(d, mail.Options{Secret: secret, SiteName: func() string { return html.UnescapeString(st.Name) }, Home: func() string { return st.Home }})
		if err := svc.SendTest(ctx, *to); err != nil {
			fmt.Fprintf(stderr, "mail test: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "test email sent to %s\n", *to)
		return 0
	}
	fs.Usage()
	return 2
}
