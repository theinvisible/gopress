package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// envBool reads a switch from the environment. A value that is set but
// not a boolean is an error: a staging switch must never be off silently
// because of "yes" or a typo.
func envBool(key string) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false, nil
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q is not a boolean (use true or false)", key, v)
}

// outboundDisabled is --disable-outbound for the subcommands besides serve
// (mail test, plugin): they follow GOPRESS_DISABLE_OUTBOUND as well.
func outboundDisabled(stderr io.Writer) (bool, bool) {
	off, err := envBool("GOPRESS_DISABLE_OUTBOUND")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return false, false
	}
	return off, true
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

// startupWarnings logs setups that work but are not safe for production,
// and the state of the staging switches.
func startupWarnings(log *slog.Logger, dataDir, home string, trustedProxies int, noIndex, noOutbound bool) {
	if os.Geteuid() == 0 {
		log.Warn("running as root: run gopress as an unprivileged user (the container image uses uid 65532)")
	}
	if fi, err := os.Stat(dataDir); err == nil && fi.Mode().Perm()&0o007 != 0 {
		log.Warn("the data directory is accessible to all users of the host; use mode 0750", "dir", dataDir, "mode", fi.Mode().Perm())
	}
	u, err := url.Parse(home)
	if err == nil && u.Scheme != "https" && !loopbackHost(u.Hostname()) {
		log.Warn("the base URL is not https: admin login needs https (secure cookies), set --base-url https://…", "base_url", home)
	}
	if err == nil && u.Scheme == "https" && trustedProxies == 0 {
		log.Warn("the base URL is https but no --trusted-proxies are set: behind a TLS proxy, client addresses are the proxy's and HSTS is not sent")
	}
	log.Info("staging switches", "noindex", noIndex, "disable_outbound", noOutbound)
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
