package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/theinvisible/gopress/internal/config"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/plugins"
	"github.com/theinvisible/gopress/internal/site"
	"github.com/theinvisible/gopress/internal/theme"
	"github.com/theinvisible/gopress/plugins/bundled"
	"github.com/theinvisible/gopress/themes"
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

const pluginUsage = `usage: gopress plugin <list|activate|deactivate> [flags] [NAME...]

Manages plugins from the command line (deployment; the admin's Plugins
screen does the same with a review of the permissions):

  list                    installed plugins, their state and permissions
  activate NAME...        approve the plugin's permissions and activate it
  deactivate NAME...      deactivate it (its data stays)

flags:
  --data DIR              data directory
  --import                with activate: import the plugin's WordPress data
                          now (otherwise run it from the Plugins screen)
`

func cmdPlugin(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("plugin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, pluginUsage) }
	cfg.RegisterFlags(fs)
	doImport := fs.Bool("import", false, "import the plugin's WordPress data")
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	sub := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if (sub == "activate" || sub == "deactivate") && fs.NArg() == 0 || sub != "list" && sub != "activate" && sub != "deactivate" {
		fs.Usage()
		return 2
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	fail := func(err error) int { fmt.Fprintf(stderr, "plugin: %v\n", err); return 1 }
	secret, err := config.LoadOrCreateSecret(filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		return fail(err)
	}
	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "gopress.db"))
	if err != nil {
		return fail(err)
	}
	defer d.Close()
	th, err := theme.Load(themes.FS, "twentytwelve")
	if err != nil {
		return fail(err)
	}
	pub := site.New(d, th, site.Options{Secret: secret, Log: log, UploadsDir: filepath.Join(cfg.DataDir, "uploads")})
	store, err := plugins.OpenStore(filepath.Join(cfg.DataDir, "plugins"), bundled.FS(), log)
	if err != nil {
		return fail(err)
	}
	defer store.Close()
	host, err := plugins.New(ctx, plugins.Options{DB: d, Store: store, Site: pub, Log: log,
		CacheDir: filepath.Join(cfg.DataDir, "cache", "wasm")})
	if err != nil {
		return fail(err)
	}
	defer host.Close(ctx)

	if sub == "list" {
		list, err := host.List(ctx)
		if err != nil {
			return fail(err)
		}
		for _, in := range list {
			state := "inactive"
			if in.State != nil && in.State.Active {
				state = "active"
				if !in.Approved {
					state = "active, update needs approval"
				}
			}
			src := in.Package.Source
			fmt.Fprintf(stdout, "%s %s (%s, %s)\n", in.Package.Manifest.Name, in.Package.Manifest.Version, src, state)
			for _, p := range in.Package.Manifest.Permissions.Describe() {
				fmt.Fprintf(stdout, "    %s\n", p)
			}
		}
		return 0
	}
	for _, name := range fs.Args() {
		audit := func(action string, detail map[string]any) {
			detail["name"], detail["via"] = name, "cli"
			b, _ := json.Marshal(detail)
			db.InsertAudit(ctx, d.W, &db.AuditEntry{Time: time.Now().Unix(), Action: action, ObjectType: "plugin", Detail: string(b)})
		}
		if sub == "deactivate" {
			if err := host.Deactivate(ctx, name); err != nil {
				return fail(err)
			}
			audit("plugin_deactivated", map[string]any{})
			fmt.Fprintf(stdout, "%s deactivated\n", name)
			continue
		}
		pkg := store.Get(name)
		if pkg == nil {
			return fail(fmt.Errorf("no plugin %q (gopress plugin list)", name))
		}
		if err := host.Activate(ctx, name, pkg.SHA256); err != nil {
			return fail(err)
		}
		audit("plugin_activated", map[string]any{"version": pkg.Manifest.Version, "sha256": pkg.SHA256,
			"permissions": pkg.Manifest.Permissions.Describe()})
		fmt.Fprintf(stdout, "%s %s activated with these permissions:\n", name, pkg.Manifest.Version)
		for _, p := range pkg.Manifest.Permissions.Describe() {
			fmt.Fprintf(stdout, "    %s\n", p)
		}
		if *doImport && host.HasImport(ctx, name) {
			notice, err := host.Migrate(ctx, name)
			if err != nil {
				return fail(fmt.Errorf("%s: import: %s", name, plugins.ErrorMessage(err)))
			}
			fmt.Fprintf(stdout, "%s: %s\n", name, notice)
		}
	}
	return 0
}
