package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("GET /healthz = %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}

func TestUnknownPathIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", rec.Code)
	}
}

func TestRunCommands(t *testing.T) {
	tests := []struct {
		args       []string
		wantCode   int
		wantStdout string
	}{
		{[]string{"version"}, 0, "gopress "},
		{[]string{"import-wp"}, 2, ""},
		{[]string{"bogus"}, 2, ""},
		{nil, 2, ""},
	}
	for _, tt := range tests {
		var out, errb bytes.Buffer
		if code := run(tt.args, &out, &errb); code != tt.wantCode {
			t.Errorf("run(%v) = %d, want %d", tt.args, code, tt.wantCode)
		}
		if !strings.Contains(out.String(), tt.wantStdout) {
			t.Errorf("run(%v) stdout = %q, want substring %q", tt.args, out.String(), tt.wantStdout)
		}
	}
}
