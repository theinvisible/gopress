// Package plugintest builds the test plugin (internal/plugins/testdata/
// tester) for tests; pack it with plugins.PackDir(Tester(t)).
package plugintest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	once      sync.Once
	src, wasm string
	err       error
	out       []byte
)

// Tester builds the test plugin once per test binary and returns its
// source directory and the compiled plugin.wasm.
func Tester(t testing.TB) (dir, wasmFile string) {
	t.Helper()
	once.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		src = filepath.Join(filepath.Dir(file), "..", "testdata", "tester")
		tmp, e := os.MkdirTemp("", "gopress-tester")
		if e != nil {
			err = e
			return
		}
		wasm = filepath.Join(tmp, "plugin.wasm")
		cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", wasm, ".")
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=")
		out, err = cmd.CombinedOutput()
	})
	if err != nil {
		t.Fatalf("building the test plugin: %v\n%s", err, out)
	}
	return src, wasm
}
