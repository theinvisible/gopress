// Package buildinfo exposes version information injected at build time.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set via -ldflags "-X github.com/theinvisible/gopress/internal/buildinfo.Version=...".
var (
	Version = ""
	Commit  = ""
)

// String returns a human-readable version line, falling back to the module
// build info when no ldflags were given (e.g. `go run`).
func String() string {
	v, c := Version, Commit
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "" {
			v = bi.Main.Version
		}
		if c == "" {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					c = s.Value
				}
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	if c == "" {
		c = "unknown"
	}
	return fmt.Sprintf("gopress %s (commit %s, %s %s/%s)", v, c, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
