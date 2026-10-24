package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"

	"github.com/theinvisible/gopress/internal/config"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/media"
	"github.com/theinvisible/gopress/internal/site"
)

const mediaUsage = `usage: gopress media <command> [flags]

commands:
  regenerate [--missing]    create the image sizes again (after changing them in
                            Settings > Media); --missing only for images that lack
                            a size
  strip-metadata [--apply]  list uploaded images that still contain metadata
                            (EXIF/GPS, XMP, IPTC, comments); --apply removes it
                            in place (lossless)

A running server picks up the changes after its next content change or restart.
`

// cliSite provides the settings to the content service outside the server.
type cliSite struct{ d *db.DB }

func (s cliSite) Settings(ctx context.Context) (*site.Settings, error) { return site.LoadSettings(ctx, s.d.R) }
func (s cliSite) Invalidate()                                         {}

func cmdMedia(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("media", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, mediaUsage) }
	cfg.RegisterFlags(fs)
	missing := fs.Bool("missing", false, "only images without all sizes (regenerate)")
	apply := fs.Bool("apply", false, "remove the metadata (strip-metadata)")
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	sub := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "gopress.db"))
	if err != nil {
		fmt.Fprintf(stderr, "media: %v\n", err)
		return 1
	}
	defer d.Close()
	store, err := media.Open(filepath.Join(cfg.DataDir, "uploads"))
	if err != nil {
		fmt.Fprintf(stderr, "media: %v\n", err)
		return 1
	}
	defer store.Close()

	switch sub {
	case "regenerate":
		svc := content.New(d, cliSite{d}, nil, slog.New(slog.NewTextHandler(stderr, nil)))
		svc.SetMediaStore(store)
		n, failed := 0, 0
		err := svc.RegenerateSizes(ctx, *missing, func(file string, err error) {
			n++
			if err != nil {
				failed++
				fmt.Fprintf(stderr, "%s: %v\n", file, err)
				return
			}
			fmt.Fprintf(stdout, "%s\n", file)
		})
		if err != nil {
			fmt.Fprintf(stderr, "media: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%d images processed, %d failed\n", n, failed)
		if failed > 0 {
			return 1
		}
		return 0
	case "strip-metadata":
		ids, err := db.AllMediaIDs(ctx, d.R)
		if err != nil {
			fmt.Fprintf(stderr, "media: %v\n", err)
			return 1
		}
		found := 0
		for _, id := range ids {
			m, err := db.GetMedia(ctx, d.R, id)
			if err != nil || m == nil {
				continue
			}
			dir := path.Dir(m.File)
			files := []string{m.File}
			if m.Original != "" {
				files = append(files, path.Join(dir, m.Original))
			}
			for _, s := range m.Sizes {
				files = append(files, path.Join(dir, s.File))
			}
			for _, f := range files {
				changed, err := store.Strip(f, *apply)
				if err != nil {
					fmt.Fprintf(stderr, "%s: %v\n", f, err)
					continue
				}
				if changed {
					found++
					fmt.Fprintln(stdout, f)
				}
			}
		}
		verb := "contain metadata (run with --apply to remove it)"
		if *apply {
			verb = "cleaned"
		}
		fmt.Fprintf(stdout, "%d files %s\n", found, verb)
		return 0
	}
	fs.Usage()
	return 2
}
