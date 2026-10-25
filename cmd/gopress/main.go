// Command gopress is a single-binary WordPress alternative.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zones without the system database (container)

	"github.com/theinvisible/gopress/internal/admin"
	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/buildinfo"
	"github.com/theinvisible/gopress/internal/config"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/live"
	"github.com/theinvisible/gopress/internal/mail"
	"github.com/theinvisible/gopress/internal/mcpserver"
	"github.com/theinvisible/gopress/internal/media"
	"github.com/theinvisible/gopress/internal/plugins"
	"github.com/theinvisible/gopress/internal/site"
	"github.com/theinvisible/gopress/internal/theme"
	"github.com/theinvisible/gopress/internal/wpimport"
	"github.com/theinvisible/gopress/plugins/bundled"
	"github.com/theinvisible/gopress/themes"
)

const usage = `usage: gopress <command> [flags]

commands:
  serve        run the web server
  import-wp    import a WordPress site (M1)
  backup       create a backup of the data directory (M5)
  user         manage users and reset passwords or two-factor authentication
  mail         show the mail queue and send a test email
  media        regenerate image sizes, remove metadata from old uploads
  plugin-pack  build a plugin package (.zip) from its source directory
  version      print version information
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return cmdServe(rest, stderr)
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	case "import-wp":
		return cmdImportWP(rest, stdout, stderr)
	case "user":
		return cmdUser(rest, os.Stdin, stdout, stderr)
	case "mail":
		return cmdMail(rest, stdout, stderr)
	case "media":
		return cmdMedia(rest, stdout, stderr)
	case "plugin-pack":
		return cmdPluginPack(rest, stdout, stderr)
	case "backup":
		fmt.Fprintf(stderr, "gopress %s: not implemented yet\n", cmd)
		return 2
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "gopress: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func cmdServe(args []string, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg.RegisterFlags(fs)
	baseURL := fs.String("base-url", os.Getenv("GOPRESS_BASE_URL"), "public base URL (default: the home option)")
	cacheMB := fs.Int("cache-mb", 256, "page cache size in MiB (0 disables)")
	trustedProxies := fs.String("trusted-proxies", os.Getenv("GOPRESS_TRUSTED_PROXIES"),
		"comma-separated CIDRs of reverse proxies whose X-Real-Ip/X-Forwarded-For are trusted, e.g. 172.16.0.0/12")
	mailSink := fs.String("mail-sink", "", "development: write outgoing mail as .eml files into this directory instead of sending it")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	var proxies []netip.Prefix
	for _, p := range strings.Split(*trustedProxies, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			fmt.Fprintf(stderr, "serve: invalid --trusted-proxies entry %q\n", p)
			return 2
		}
		proxies = append(proxies, prefix)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		log.Error("data dir", "err", err)
		return 1
	}
	secret, err := config.LoadOrCreateSecret(filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		log.Error("secret", "err", err)
		return 1
	}
	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "gopress.db"))
	if err != nil {
		log.Error("open database", "err", err)
		return 1
	}
	defer d.Close()
	th, err := theme.Load(themes.FS, "twentytwelve")
	if err != nil {
		log.Error("load theme", "err", err)
		return 1
	}
	public := site.New(d, th, site.Options{
		BaseURL:        *baseURL,
		UploadsDir:     filepath.Join(cfg.DataDir, "uploads"),
		Secret:         secret,
		CacheBytes:     int64(*cacheMB) << 20,
		TrustedProxies: proxies,
		Log:            log,
	})

	settings := func() *site.Settings {
		st, err := public.Settings(context.Background())
		if err != nil {
			return &site.Settings{Name: "GoPress", Home: *baseURL}
		}
		return st
	}
	if *mailSink != "" {
		if err := os.MkdirAll(*mailSink, 0o700); err != nil {
			log.Error("mail sink", "err", err)
			return 1
		}
		log.Warn("development mode: mail is written to the sink directory, not sent", "dir", *mailSink)
	}
	mailSvc := mail.New(d, mail.Options{
		Secret:   secret,
		Home:     func() string { return settings().Home },
		SiteName: func() string { return html.UnescapeString(settings().Name) },
		Sink:     *mailSink,
		Log:      log,
	})
	authSvc := auth.New(d, auth.Options{
		Secret:         secret,
		Home:           func() string { return settings().Home },
		SiteName:       func() string { return settings().Name },
		TrustedProxies: proxies,
		Log:            log,
		Notify: func(ctx context.Context, to string, data map[string]any) {
			if err := mailSvc.Enqueue(ctx, to, "security", data); err != nil {
				log.Warn("security notice", "err", err)
			}
		},
	})
	contentSvc := content.New(d, public, authSvc, log)
	contentSvc.SetMailer(mailSvc)
	store, err := media.Open(filepath.Join(cfg.DataDir, "uploads"))
	if err != nil {
		log.Error("uploads", "err", err)
		return 1
	}
	defer store.Close()
	contentSvc.SetMediaStore(store)
	public.SetCommentNotifier(contentSvc.NotifyComment)
	pluginStore, err := plugins.OpenStore(filepath.Join(cfg.DataDir, "plugins"), bundled.FS(), log)
	if err != nil {
		log.Error("plugins", "err", err)
		return 1
	}
	defer pluginStore.Close()
	liveHub := live.NewHub()
	contentSvc.SetLive(liveHub)
	pluginHost, err := plugins.New(ctx, plugins.Options{Live: liveHub, DB: d, Store: pluginStore, Site: public, Log: log,
		CacheDir: filepath.Join(cfg.DataDir, "cache", "wasm"), ClientIP: authSvc.ClientIP, Mail: mailSvc})
	if err != nil {
		log.Error("plugins", "err", err)
		return 1
	}
	defer pluginHost.Close(context.Background())
	public.SetExtensions(pluginHost)
	public.SetCommentFilter(pluginHost.CommentFilter())
	contentSvc.SetCommentStatusHook(pluginHost.CommentStatusChanged)
	content.SetPluginShortcodes(pluginHost.ShortcodeNames)
	adm, err := admin.New(d, authSvc, public, contentSvc, admin.Options{Log: log, Mail: mailSvc, Live: liveHub, MailSink: *mailSink != "",
		Plugins: pluginHost})
	if err != nil {
		log.Error("admin", "err", err)
		return 1
	}
	public.SetPreviewer(adm.Previewer())
	mux := newMux(public)
	adm.Register(mux)
	mux.Handle("/mcp", mcpserver.New(mcpserver.Options{DB: d, Auth: authSvc, Content: contentSvc, Site: public, Next: public, Log: log}))
	if st := settings(); st.MCPEnabled && !mcpserver.SecureHome(st.Home) {
		log.Warn("the MCP interface is on but the base URL is not https; it stays off", "base_url", st.Home)
	}
	go authSvc.Maintain(ctx)
	go contentSvc.RunScheduler(ctx)
	go mailSvc.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           site.SecurityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	srv.RegisterOnShutdown(adm.StopLive)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr, "data", cfg.DataDir, "version", buildinfo.String())

	select {
	case err := <-errc:
		log.Error("server failed", "err", err)
		return 1
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
		return 1
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "err", err)
		return 1
	}
	return 0
}

func cmdImportWP(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	var opts wpimport.Options
	fs := flag.NewFlagSet("import-wp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg.RegisterFlags(fs)
	fs.StringVar(&opts.DSN, "dsn", "", "WordPress database DSN, e.g. user:pass@tcp(127.0.0.1:3306)/wordpress")
	fs.StringVar(&opts.Prefix, "prefix", "wp_", "WordPress table prefix")
	fs.StringVar(&opts.UploadsDir, "uploads", "", "WordPress wp-content/uploads directory to copy")
	fs.BoolVar(&opts.Force, "force", false, "replace an existing GoPress database")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if opts.DSN == "" {
		fmt.Fprintln(stderr, "import-wp: --dsn is required")
		return 2
	}
	opts.DataDir = cfg.DataDir
	opts.Log = slog.New(slog.NewTextHandler(stderr, nil))

	rep, err := wpimport.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(stderr, "import-wp: %v\n", err)
		return 1
	}
	keys := make([]string, 0, len(rep.Counts))
	for k := range rep.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(stdout, "%-40s %6d\n", k, rep.Counts[k])
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	return 0
}

func newMux(public http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok")
	})
	mux.Handle("/", public)
	return mux
}
