package theme

import (
	"testing"
	"time"
)

func TestFormatPHPDate(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Vienna")
	tm := time.Date(2026, 7, 18, 11, 16, 15, 0, loc)
	tests := map[string]string{
		"d/m/Y":            "18/07/2026",
		"H:i":              "11:16",
		"c":                "2026-07-18T11:16:15+02:00",
		"F Y":              "July 2026",
		"jS \\o\\f F":      "18th of July",
		"g:i a":            "11:16 am",
		"D, d M Y H:i:s O": "Sat, 18 Jul 2026 11:16:15 +0200",
	}
	for f, want := range tests {
		if got := FormatPHPDate(f, tm); got != want {
			t.Errorf("FormatPHPDate(%q) = %q, want %q", f, got, want)
		}
	}
}
