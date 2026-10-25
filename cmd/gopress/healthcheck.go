package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/theinvisible/gopress/internal/config"
)

// cmdHealthcheck asks the local server for /healthz. Container images have
// no curl, so the Docker HEALTHCHECK runs this; exit code 0 means healthy.
func cmdHealthcheck(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg.RegisterFlags(fs)
	timeout := fs.Duration("timeout", 3*time.Second, "time to wait for the answer")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	target, err := healthURL(cfg.Addr)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 2
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

// healthURL turns a listen address into the URL of /healthz on this
// host: an unspecified or empty host becomes the loopback address.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
