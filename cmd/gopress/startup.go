package main

import (
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
)

func envBool(key string) bool {
	b, _ := strconv.ParseBool(os.Getenv(key))
	return b
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

// startupWarnings logs setups that work but are not safe for production.
func startupWarnings(log *slog.Logger, dataDir, home string, noIndex bool) {
	if os.Geteuid() == 0 {
		log.Warn("running as root: run gopress as an unprivileged user (the container image uses uid 65532)")
	}
	if fi, err := os.Stat(dataDir); err == nil && fi.Mode().Perm()&0o007 != 0 {
		log.Warn("the data directory is accessible to all users of the host; use mode 0750", "dir", dataDir, "mode", fi.Mode().Perm())
	}
	if u, err := url.Parse(home); err == nil && u.Scheme != "https" && !loopbackHost(u.Hostname()) {
		log.Warn("the base URL is not https: admin login needs https (secure cookies), set --base-url https://…", "base_url", home)
	}
	if noIndex {
		log.Info("search engines are kept out (--noindex)")
	}
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
