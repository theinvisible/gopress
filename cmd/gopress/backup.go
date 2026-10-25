package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/theinvisible/gopress/internal/backup"
	"github.com/theinvisible/gopress/internal/config"
)

// cmdBackup writes a backup archive of the data directory. It can run
// while the server is running (docker exec).
func cmdBackup(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg.RegisterFlags(fs)
	out := fs.String("out", os.Getenv("GOPRESS_BACKUP_DIR"), "directory for the backup archives, outside the data directory (env GOPRESS_BACKUP_DIR)")
	keep := fs.Int("keep", 0, "delete the oldest archives in --out so that this many remain (0 keeps all)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || *keep < 0 {
		fmt.Fprintln(stderr, "backup: --out is required and --keep must not be negative")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := backup.Create(ctx, backup.Options{DataDir: cfg.DataDir, OutDir: *out, Keep: *keep})
	if err != nil {
		fmt.Fprintf(stderr, "backup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s (%d files, %.1f MiB)\n", res.Path, res.Files, float64(res.Size)/(1<<20))
	for _, r := range res.Removed {
		fmt.Fprintf(stdout, "removed old backup %s\n", r)
	}
	return 0
}

// cmdRestore unpacks a backup into an empty data directory.
func cmdRestore(args []string, stdout, stderr io.Writer) int {
	var cfg config.Config
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg.RegisterFlags(fs)
	from := fs.String("from", "", "backup archive (gopress-*.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *from == "" {
		fmt.Fprintln(stderr, "restore: --from is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	man, err := backup.Restore(ctx, backup.RestoreOptions{From: *from, DataDir: cfg.DataDir})
	if err != nil {
		fmt.Fprintf(stderr, "restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "restored the backup of %s (%s) into %s\n", man.Created, man.Version, cfg.DataDir)
	return 0
}
