package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/config"
	"github.com/theinvisible/gopress/internal/db"
)

// newData creates a data directory with a database, a secret, uploads,
// a plugin, a cache and temporary files.
func newData(t *testing.T) (string, *db.DB) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadOrCreateSecret(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), filepath.Join(dir, "gopress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for p, body := range map[string]string{
		"uploads/2026/10/photo.jpg":     "jpeg",
		"uploads/2026/10/photo-150.jpg": "small",
		"uploads/.tmp/upload-1":         "partial",
		"uploads/2026/10/x.jpg.tmp":     "partial",
		"plugins/hello.zip":             "zip",
		"cache/wasm/abc":                "compiled",
	} {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o750)
		if err := os.WriteFile(full, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// A link out of the data directory must not be followed.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("outside"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "uploads", "link.txt")); err != nil {
		t.Fatal(err)
	}
	return dir, d
}

func setOption(t *testing.T, d *db.DB, name, value string) {
	t.Helper()
	if _, err := d.W.Exec(`INSERT INTO options (name, value) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
		t.Fatal(err)
	}
}

func list(t *testing.T, archive string) map[string]string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		var b bytes.Buffer
		b.ReadFrom(tr)
		out[h.Name] = fmt.Sprintf("%o", h.Mode)
	}
	return out
}

func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	dir, d := newData(t)
	setOption(t, d, "blogname", "Before")

	// Writes keep going while the backup runs.
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			d.W.Exec(`INSERT INTO options (name, value) VALUES (?, ?)`, fmt.Sprintf("load_%d", i), "x")
		}
	}()
	out := filepath.Join(t.TempDir(), "backups")
	res, err := Create(ctx, Options{DataDir: dir, OutDir: out})
	stop.Store(true)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(res.Path)
	if err != nil || fi.Mode().Perm() != 0o600 || !reName.MatchString(filepath.Base(res.Path)) {
		t.Fatalf("archive %s: %v %v", res.Path, fi.Mode(), err)
	}
	entries := list(t, res.Path)
	for _, want := range []string{"gopress-backup.json", "secret.key", "gopress.db", "uploads/2026/10/photo.jpg",
		"uploads/2026/10/photo-150.jpg", "plugins/hello.zip"} {
		if _, ok := entries[want]; !ok {
			t.Errorf("%s missing from the backup: %v", want, entries)
		}
	}
	for name := range entries {
		if strings.HasPrefix(name, "cache") || strings.Contains(name, ".tmp") || strings.Contains(name, "link.txt") ||
			strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			t.Errorf("%s must not be in the backup", name)
		}
	}
	if entries["secret.key"] != "600" {
		t.Errorf("secret mode %s", entries["secret.key"])
	}
	left, _ := os.ReadDir(out)
	if len(left) != 1 {
		t.Errorf("temporary files left: %v", left)
	}

	setOption(t, d, "blogname", "After")
	target := filepath.Join(t.TempDir(), "restored")
	man, err := Restore(ctx, RestoreOptions{From: res.Path, DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	if man.Format != Format || man.Schema != db.SchemaVersion() {
		t.Errorf("manifest %+v", man)
	}
	r, err := db.Open(ctx, filepath.Join(target, "gopress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var name string
	r.R.QueryRow(`SELECT value FROM options WHERE name = 'blogname'`).Scan(&name)
	if name != "Before" {
		t.Errorf("restored blogname %q", name)
	}
	orig, _ := os.ReadFile(filepath.Join(dir, "secret.key"))
	got, _ := os.ReadFile(filepath.Join(target, "secret.key"))
	if !bytes.Equal(orig, got) {
		t.Error("secret differs")
	}
	if _, err := config.LoadOrCreateSecret(filepath.Join(target, "secret.key")); err != nil {
		t.Errorf("restored secret not accepted: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "uploads/2026/10/photo.jpg")); string(b) != "jpeg" {
		t.Errorf("upload %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(target, "uploads/2026/10/photo.jpg")); fi.Mode().Perm() != 0o640 {
		t.Errorf("upload mode %v", fi.Mode())
	}

	// Never into a directory that holds something.
	if _, err := Restore(ctx, RestoreOptions{From: res.Path, DataDir: target}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("restore over data: %v", err)
	}
}

func TestBackupRefusals(t *testing.T) {
	ctx := context.Background()
	dir, _ := newData(t)
	if _, err := Create(ctx, Options{DataDir: dir, OutDir: filepath.Join(dir, "backups")}); err == nil {
		t.Error("backup into the data directory accepted")
	}
	os.Chmod(filepath.Join(dir, "secret.key"), 0o600)
	os.Remove(filepath.Join(dir, "secret.key"))
	if _, err := Create(ctx, Options{DataDir: dir, OutDir: t.TempDir()}); err == nil {
		t.Error("backup without a secret accepted")
	}
}

func TestRetention(t *testing.T) {
	ctx := context.Background()
	dir, _ := newData(t)
	out := t.TempDir()
	foreign := []string{"notes.txt", "gopress-latest.tar.gz", "gopress-20200101T000000Z.tar.gz.tmp"}
	for _, n := range foreign {
		os.WriteFile(filepath.Join(out, n), []byte("keep"), 0o600)
	}
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	var paths []string
	for i := range 4 {
		now := start.Add(time.Duration(i) * 24 * time.Hour)
		res, err := Create(ctx, Options{DataDir: dir, OutDir: out, Keep: 2, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, res.Path)
	}
	for i, p := range paths {
		_, err := os.Stat(p)
		if (i >= 2) != (err == nil) {
			t.Errorf("archive %d: %v", i, err)
		}
	}
	for _, n := range foreign {
		if _, err := os.Stat(filepath.Join(out, n)); err != nil {
			t.Errorf("retention removed %s", n)
		}
	}
	// The same second twice: refused, nothing overwritten.
	now := start.Add(3 * 24 * time.Hour)
	if _, err := Create(ctx, Options{DataDir: dir, OutDir: out, Now: func() time.Time { return now }}); err == nil {
		t.Error("existing archive overwritten")
	}
}

type entry struct {
	name string
	typ  byte
	body string
	link string
}

func archive(t *testing.T, entries []entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "evil.tar.gz")
	os.WriteFile(p, buf.Bytes(), 0o600)
	return p
}

func TestRestoreRefusesHostileArchives(t *testing.T) {
	ctx := context.Background()
	// A real database and manifest so only the hostile entry fails.
	dir, _ := newData(t)
	res, err := Create(ctx, Options{DataDir: dir, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var good []entry
	{
		f, _ := os.Open(res.Path)
		gz, _ := gzip.NewReader(f)
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			var b bytes.Buffer
			b.ReadFrom(tr)
			if h.Typeflag == tar.TypeReg && !strings.Contains(h.Name, "/") {
				good = append(good, entry{name: h.Name, typ: tar.TypeReg, body: b.String()})
			}
		}
		f.Close()
	}
	newer, _ := json.Marshal(Manifest{Format: Format, Schema: db.SchemaVersion() + 1})
	futureFormat, _ := json.Marshal(Manifest{Format: Format + 1})
	withManifest := func(m []byte, rest ...entry) []entry {
		var out []entry
		for _, e := range good {
			if e.name == manifestName {
				e.body = string(m)
			}
			out = append(out, e)
		}
		return append(out, rest...)
	}
	cases := map[string][]entry{
		"traversal":      append(append([]entry{}, good...), entry{name: "uploads/../../evil", typ: tar.TypeReg, body: "x"}),
		"absolute":       append(append([]entry{}, good...), entry{name: "/etc/evil", typ: tar.TypeReg, body: "x"}),
		"dotdot":         append(append([]entry{}, good...), entry{name: "../evil", typ: tar.TypeReg, body: "x"}),
		"symlink":        append(append([]entry{}, good...), entry{name: "uploads/l", typ: tar.TypeSymlink, link: "/etc/passwd"}),
		"hardlink":       append(append([]entry{}, good...), entry{name: "uploads/h", typ: tar.TypeLink, link: "secret.key"}),
		"unknown top":    append(append([]entry{}, good...), entry{name: "cron.sh", typ: tar.TypeReg, body: "x"}),
		"duplicate":      append(append([]entry{}, good...), entry{name: "secret.key", typ: tar.TypeReg, body: "x"}),
		"device":         append(append([]entry{}, good...), entry{name: "uploads/dev", typ: tar.TypeChar}),
		"file as tree":   append(append([]entry{}, good...), entry{name: "uploads", typ: tar.TypeReg, body: "x"}),
		"no manifest":    good[1:],
		"newer schema":   withManifest(newer),
		"future format":  withManifest(futureFormat),
		"corrupt db":     append([]entry{good[0], good[1]}, entry{name: "gopress.db", typ: tar.TypeReg, body: "not sqlite"}),
		"missing secret": {good[0], good[2]},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "data")
			_, err := Restore(ctx, RestoreOptions{From: archive(t, entries), DataDir: target})
			if err == nil {
				t.Fatal("accepted")
			}
			t.Log(err)
			if left, _ := os.ReadDir(target); len(left) != 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
	// Size limit.
	target := filepath.Join(t.TempDir(), "data")
	if _, err := Restore(ctx, RestoreOptions{From: res.Path, DataDir: target, MaxBytes: 100}); err == nil {
		t.Error("size limit ignored")
	}
}

func TestReviewFindings(t *testing.T) {
	ctx := context.Background()
	dir, _ := newData(t)

	// A link (or another path) to a directory inside the data directory.
	link := filepath.Join(t.TempDir(), "out")
	if err := os.Symlink(filepath.Join(dir, "uploads"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, Options{DataDir: dir, OutDir: filepath.Join(link, "bk")}); err == nil {
		t.Error("backup through a link into the data directory accepted")
	}
	dataLink := filepath.Join(t.TempDir(), "data")
	os.Symlink(dir, dataLink)
	if _, err := Create(ctx, Options{DataDir: dataLink, OutDir: filepath.Join(dir, "uploads", "bk")}); err == nil {
		t.Error("backup into the data directory given through a link accepted")
	}

	// Retention never deletes the archive just written, and stale
	// temporary files of killed backups go away.
	out := t.TempDir()
	future := filepath.Join(out, "gopress-29990101T000000Z.tar.gz")
	os.WriteFile(future, []byte("x"), 0o600)
	stale := filepath.Join(out, ".gopress-20200101T000000Z.db.tmp")
	fresh := filepath.Join(out, "gopress-20200101T000001Z.tar.gz.tmp")
	os.WriteFile(stale, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(stale, old, old)
	res, err := Create(ctx, Options{DataDir: dir, OutDir: out, Keep: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Error("the new backup was pruned")
	}
	if _, err := os.Stat(future); err == nil {
		t.Error("the older (future-dated) archive was kept")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale temporary file kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a recent temporary file (backup in progress) was removed")
	}

	// Nested entries only below the backed-up trees.
	for _, name := range []string{"gopress-backup.json/x", "secret.key/x", "gopress.db/x/y"} {
		if _, err := checkName(name); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := checkName("uploads/2026/10/a.jpg"); err != nil {
		t.Error(err)
	}
}
