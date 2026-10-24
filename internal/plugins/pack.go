package plugins

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PackDir builds a plugin package from a source directory (plugin.json,
// assets/, admin/, README/LICENSE) and a compiled plugin.wasm. The zip is
// deterministic (sorted, fixed times) and validated like an upload.
func PackDir(dir, wasmFile string) ([]byte, error) {
	files := map[string]string{"plugin.wasm": wasmFile}
	for name := range rootFiles {
		if name == "plugin.wasm" {
			continue
		}
		if p := filepath.Join(dir, name); fileExists(p) {
			files[name] = p
		}
	}
	for _, sub := range []string{"assets", "admin"} {
		root := filepath.Join(dir, sub)
		if !fileExists(root) {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = p
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, n := range names {
		data, err := os.ReadFile(files[n])
		if err != nil {
			return nil, err
		}
		method := zip.Deflate
		if strings.HasSuffix(n, ".png") || strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".woff2") {
			method = zip.Store
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: n, Method: method, Modified: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if _, err := ReadZip(b.Bytes(), "uploaded"); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
