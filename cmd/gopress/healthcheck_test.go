package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthURL(t *testing.T) {
	for addr, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		"[::]:8080":      "http://[::1]:8080/healthz",
		"127.0.0.1:9000": "http://127.0.0.1:9000/healthz",
		"10.0.0.5:80":    "http://10.0.0.5:80/healthz",
	} {
		if got, err := healthURL(addr); err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := healthURL("8080"); err == nil {
		t.Error("an address without a port was accepted")
	}
}

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(newMux(http.NotFoundHandler()))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	var out, errb bytes.Buffer
	if code := run([]string{"healthcheck", "--addr", addr}, &out, &errb); code != 0 || out.String() != "ok\n" {
		t.Errorf("healthy: %d %q %q", code, out.String(), errb.String())
	}

	// Not 200 (and no redirect followed), or nothing listening: unhealthy.
	bad := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer bad.Close()
	if code := run([]string{"healthcheck", "--addr", strings.TrimPrefix(bad.URL, "http://")}, &out, &errb); code != 1 {
		t.Errorf("redirect: %d", code)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	if code := run([]string{"healthcheck", "--addr", closed, "--timeout", "1s"}, &out, &errb); code != 1 {
		t.Errorf("nothing listening: %d", code)
	}
}
