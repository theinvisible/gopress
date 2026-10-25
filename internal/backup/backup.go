// Package backup creates and restores backups of a GoPress data directory.
//
// A backup is one tar.gz file holding a consistent copy of the database
// (VACUUM INTO, so the server keeps running), the site secret, the uploads,
// uploaded plugins and theme overrides. Caches and temporary files are left
// out. Restoring goes only into an empty directory and accepts nothing but
// regular files and directories below the known top-level names.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/buildinfo"
	"github.com/theinvisible/gopress/internal/db"
)

// Format is the version of the archive layout.
const Format = 1

const (
	manifestName = "gopress-backup.json"
	dbName       = "gopress.db"
	secretName   = "secret.key"
)

// trees are the directories of the data directory that are backed up.
// cache/ (compiled plugins) is rebuilt on start and left out.
var trees = []string{"uploads", "plugins", "themes"}

// reName matches the archives Create writes; retention touches no other file.
var reName = regexp.MustCompile(`^gopress-\d{8}T\d{6}Z\.tar\.gz$`)

// Manifest describes a backup.
type Manifest struct {
	Format  int    `json:"format"`
	Created string `json:"created"`
	Version string `json:"version"`
	Schema  int    `json:"schema"`
}

// Options configure Create.
type Options struct {
	DataDir string
	OutDir  string
	Keep    int // keep this many archives in OutDir (0 = all)
	Now     func() time.Time
}

// Result describes a written backup.
type Result struct {
	Path    string
	Size    int64
	Files   int
	Removed []string // old archives deleted by retention
}

// Create writes a backup of opts.DataDir into opts.OutDir.
func Create(ctx context.Context, opts Options) (*Result, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	data, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, err
	}
	out, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	inside, err := within(out, data)
	if err != nil {
		return nil, err
	}
	if inside {
		return nil, errors.New("the backup directory must be outside the data directory")
	}
	cleanStale(out, now())
	root, err := os.OpenRoot(data)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	secret, err := readSecret(root)
	if err != nil {
		return nil, err
	}

	stamp := now().UTC().Format("20060102T150405Z")
	name := "gopress-" + stamp + ".tar.gz"
	final := filepath.Join(out, name)
	if _, err := os.Lstat(final); err == nil {
		return nil, fmt.Errorf("%s already exists", final)
	}

	// The database first: the archive then holds every upload it refers to.
	snap := filepath.Join(out, ".gopress-"+stamp+".db.tmp")
	if err := db.Snapshot(ctx, filepath.Join(data, dbName), snap); err != nil {
		return nil, err
	}
	defer os.Remove(snap)
	schema, err := db.Check(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("the database copy is not usable: %w", err)
	}

	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	w := &writer{tw: tw, mtime: now().UTC().Truncate(time.Second)}

	man, _ := json.MarshalIndent(Manifest{Format: Format, Created: now().UTC().Format(time.RFC3339),
		Version: buildinfo.String(), Schema: schema}, "", "  ")
	if err := w.bytes(manifestName, man, 0o600); err != nil {
		return nil, err
	}
	if err := w.bytes(secretName, secret, 0o600); err != nil {
		return nil, err
	}
	if err := w.file(dbName, snap, 0o600); err != nil {
		return nil, err
	}
	for _, t := range trees {
		if err := w.tree(ctx, root, t); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	ok = true
	res := &Result{Path: final, Size: fi.Size(), Files: w.files}
	if opts.Keep > 0 {
		res.Removed, err = prune(out, opts.Keep, name)
	}
	return res, err
}

// within reports whether dir is data or below it, comparing the actual
// directories (os.SameFile), so symbolic links and bind mounts do not hide
// it.
func within(dir, data string) (bool, error) {
	dataInfo, err := os.Stat(data)
	if err != nil {
		return false, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	for {
		fi, err := os.Stat(dir)
		if err != nil {
			return false, err
		}
		if os.SameFile(fi, dataInfo) {
			return true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

// reStale matches the temporary files of an interrupted backup.
var reStale = regexp.MustCompile(`^(\.gopress-\d{8}T\d{6}Z\.db|gopress-\d{8}T\d{6}Z\.tar\.gz)\.tmp$`)

// cleanStale removes temporary files that a killed backup left behind
// (older than a day, so a backup running in parallel keeps its files).
func cleanStale(dir string, now time.Time) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.Type().IsRegular() || !reStale.MatchString(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > 24*time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func readSecret(root *os.Root) ([]byte, error) {
	fi, err := root.Lstat(secretName)
	if err != nil {
		return nil, fmt.Errorf("site secret: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > 1<<10 {
		return nil, errors.New("site secret: not a small regular file")
	}
	return root.ReadFile(secretName)
}

type writer struct {
	tw    *tar.Writer
	mtime time.Time
	files int
}

func (w *writer) header(name string, mode int64, size int64, typ byte) error {
	return w.tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, Typeflag: typ,
		ModTime: w.mtime, Format: tar.FormatPAX})
}

func (w *writer) bytes(name string, b []byte, mode int64) error {
	if err := w.header(name, mode, int64(len(b)), tar.TypeReg); err != nil {
		return err
	}
	_, err := w.tw.Write(b)
	w.files++
	return err
}

func (w *writer) file(name, src string, mode int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return w.copy(name, f, mode)
}

func (w *writer) copy(name string, f fs.File, mode int64) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := w.header(name, mode, fi.Size(), tar.TypeReg); err != nil {
		return err
	}
	// A file that grows while it is read is cut at its size from Stat.
	n, err := io.Copy(w.tw, io.LimitReader(f, fi.Size()))
	if err != nil {
		return err
	}
	if n != fi.Size() {
		return fmt.Errorf("%s changed while it was read", name)
	}
	w.files++
	return nil
}

// tree adds a directory of the data directory. Symbolic links and other
// special files are skipped; so are temporary files.
func (w *writer) tree(ctx context.Context, root *os.Root, dir string) error {
	fi, err := root.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return nil
	}
	fsys := root.FS()
	return fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while walking
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip(p, d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return w.header(p+"/", 0o750, 0, tar.TypeDir)
		case d.Type().IsRegular():
			f, err := fsys.Open(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			defer f.Close()
			return w.copy(p, f, 0o640)
		}
		return nil // links, sockets, devices
	})
}

func skip(p string, d fs.DirEntry) bool {
	base := path.Base(p)
	if d.IsDir() {
		return p == "uploads/.tmp"
	}
	return strings.HasSuffix(base, ".tmp")
}

// prune deletes the oldest archives in dir so that keep remain; current
// (the archive just written) is never deleted, even if others look newer
// (a clock that was ahead).
func prune(dir string, keep int, current string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && reName.MatchString(e.Name()) && e.Name() != current {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // the time stamp sorts
	var removed []string
	for len(names) > keep-1 {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return removed, err
		}
		removed = append(removed, names[0])
		names = names[1:]
	}
	return removed, nil
}

// RestoreOptions configure Restore.
type RestoreOptions struct {
	From     string
	DataDir  string
	MaxBytes int64 // total size of the unpacked files (default 50 GiB)
	MaxFiles int   // number of entries (default 200000)
}

// Restore unpacks a backup into an empty (or missing) data directory and
// checks the database. On failure the directory is left empty again.
func Restore(ctx context.Context, opts RestoreOptions) (*Manifest, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 50 << 30
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 200000
	}
	if err := os.MkdirAll(opts.DataDir, 0o750); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("%s is not empty: restore only goes into an empty directory", opts.DataDir)
	}
	root, err := os.OpenRoot(opts.DataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	man, err := unpack(ctx, root, opts)
	if err == nil {
		err = verify(ctx, opts.DataDir, man)
	}
	if err != nil {
		emptyDir(opts.DataDir)
		return nil, err
	}
	return man, nil
}

// emptyDir removes what a failed restore wrote; the directory itself stays
// (it may be a mount point).
func emptyDir(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

var topLevel = []string{manifestName, dbName, secretName, "uploads", "plugins", "themes"}

// checkName validates an archive path and returns it cleaned.
func checkName(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) || path.IsAbs(name) {
		return "", fmt.Errorf("invalid path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid path %q", name)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." || part == "." || part == "" {
			return "", fmt.Errorf("invalid path %q", name)
		}
	}
	top, _, nested := strings.Cut(clean, "/")
	if !slices.Contains(topLevel, top) || nested && !slices.Contains(trees, top) {
		return "", fmt.Errorf("unexpected entry %q", name)
	}
	return clean, nil
}

func unpack(ctx context.Context, root *os.Root, opts RestoreOptions) (*Manifest, error) {
	f, err := os.Open(opts.From)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("not a backup archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var total int64
	var man *Manifest
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		if len(seen) >= opts.MaxFiles {
			return nil, errors.New("the archive has too many entries")
		}
		name, err := checkName(h.Name)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate entry %q", name)
		}
		seen[name] = true
		single := name == manifestName || name == dbName || name == secretName
		switch h.Typeflag {
		case tar.TypeDir:
			if single {
				return nil, fmt.Errorf("%s must be a file", name)
			}
			if err := mkdirAll(root, name); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if !single && !strings.Contains(name, "/") {
				return nil, fmt.Errorf("%s must be a directory", name)
			}
			if h.Size < 0 || total+h.Size > opts.MaxBytes {
				return nil, errors.New("the archive is larger than allowed")
			}
			total += h.Size
			if name == manifestName {
				man, err = readManifest(tr, h.Size)
				if err != nil {
					return nil, err
				}
				continue
			}
			if dir := path.Dir(name); dir != "." {
				if err := mkdirAll(root, dir); err != nil {
					return nil, err
				}
			}
			mode := os.FileMode(0o640)
			if name == secretName || name == dbName {
				mode = 0o600
			}
			if err := writeFile(root, name, tr, h.Size, mode); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("entry %q is not a regular file or directory", h.Name)
		}
	}
	if man == nil {
		return nil, errors.New("not a GoPress backup: no " + manifestName)
	}
	for _, need := range []string{dbName, secretName} {
		if !seen[need] {
			return nil, fmt.Errorf("the backup has no %s", need)
		}
	}
	return man, nil
}

func readManifest(r io.Reader, size int64) (*Manifest, error) {
	if size > 64<<10 {
		return nil, errors.New("manifest too large")
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(r, size)).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Format < 1 || m.Format > Format {
		return nil, fmt.Errorf("backup format %d is not supported by this gopress (knows %d)", m.Format, Format)
	}
	if m.Schema > db.SchemaVersion() {
		return nil, fmt.Errorf("the backup has schema version %d, this gopress knows only up to %d: restore it with a newer gopress", m.Schema, db.SchemaVersion())
	}
	return &m, nil
}

func mkdirAll(root *os.Root, dir string) error {
	cur := ""
	for _, part := range strings.Split(dir, "/") {
		cur = path.Join(cur, part)
		err := root.Mkdir(cur, 0o750)
		if err == nil || errors.Is(err, fs.ErrExist) {
			fi, err := root.Lstat(cur)
			if err != nil {
				return err
			}
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", cur)
			}
			continue
		}
		return err
	}
	return nil
}

func writeFile(root *os.Root, name string, r io.Reader, size int64, mode os.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, size))
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// verify checks the restored database and brings its schema up to date.
func verify(ctx context.Context, dir string, man *Manifest) error {
	p := filepath.Join(dir, dbName)
	schema, err := db.Check(ctx, p)
	if err != nil {
		return err
	}
	if schema != man.Schema {
		return fmt.Errorf("the database has schema %d, the manifest says %d", schema, man.Schema)
	}
	d, err := db.Open(ctx, p)
	if err != nil {
		return err
	}
	return d.Close()
}
