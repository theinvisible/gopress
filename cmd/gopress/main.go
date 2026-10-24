// Command gopress is a single-binary WordPress alternative.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/theinvisible/gopress/internal/buildinfo"
	"github.com/theinvisible/gopress/internal/config"
)

const usage = `usage: gopress <command> [flags]

commands:
  serve        run the web server
  import-wp    import a WordPress site (M1)
  backup       create a backup of the data directory (M5)
  user         manage users (M2)
  plugin-pack  build a plugin .zip (M3)
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
	case "import-wp", "backup", "user", "plugin-pack":
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
	if err := fs.Parse(args); err != nil {
		return 2
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           newMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><title>GoPress</title><h1>GoPress</h1><p>Under construction.</p>")
	})
	return mux
}
