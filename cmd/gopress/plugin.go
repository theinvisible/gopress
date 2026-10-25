package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/theinvisible/gopress/internal/plugins"
)

const pluginPackUsage = `usage: gopress plugin-pack [-o FILE] DIR

Builds a plugin from its source directory: compiles the Go code with the
standard toolchain (GOOS=wasip1 GOARCH=wasm, -buildmode=c-shared), checks
plugin.json and writes NAME-VERSION.zip (or FILE) with plugin.json,
plugin.wasm, assets/, admin/ and templates/. With -wasm FILE an already compiled module
is packed instead (plugins written in other languages).
`

func cmdPluginPack(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin-pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, pluginPackUsage) }
	out := fs.String("o", "", "output file")
	wasm := fs.String("wasm", "", "compiled plugin.wasm to pack instead of building")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	dir := fs.Arg(0)
	m, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		fmt.Fprintf(stderr, "plugin-pack: %v\n", err)
		return 1
	}
	manifest, err := plugins.ParseManifest(m)
	if err != nil {
		fmt.Fprintf(stderr, "plugin-pack: %v\n", err)
		return 1
	}
	if *wasm == "" {
		tmp, err := os.MkdirTemp("", "gopress-plugin")
		if err != nil {
			fmt.Fprintf(stderr, "plugin-pack: %v\n", err)
			return 1
		}
		defer os.RemoveAll(tmp)
		*wasm = filepath.Join(tmp, "plugin.wasm")
		// Reproducible: the same sources give the same module (bundled
		// plugins are committed as zips).
		cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=",
			"-o", *wasm, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=")
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(stderr, "plugin-pack: build failed: %v\n", err)
			return 1
		}
	}
	zip, err := plugins.PackDir(dir, *wasm)
	if err != nil {
		fmt.Fprintf(stderr, "plugin-pack: %v\n", err)
		return 1
	}
	if *out == "" {
		*out = manifest.Name + "-" + manifest.Version + ".zip"
	}
	if err := os.WriteFile(*out, zip, 0o644); err != nil {
		fmt.Fprintf(stderr, "plugin-pack: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s (%d KB)\n", *out, len(zip)>>10)
	return 0
}
