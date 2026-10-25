package plugins

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// Package limits.
const (
	MaxZipBytes      = 20 << 20 // uploaded file
	maxUnpackedBytes = 64 << 20 // all files together
	maxFileBytes     = 32 << 20 // one file (plugin.wasm is the largest)
	maxEntries       = 500
)

// Package is a validated plugin package held in memory.
type Package struct {
	Manifest *Manifest
	SHA256   string // of the zip file
	Source   string // "bundled" or "uploaded"
	Wasm     []byte
	files    map[string][]byte // assets/…, admin/… and templates/…
}

// File returns a file of the package below assets/ or admin/.
func (p *Package) File(name string) ([]byte, bool) {
	b, ok := p.files[name]
	return b, ok
}

// assetExt are the file types of assets/ and admin/ (admin/ also html).
var assetExt = map[string]bool{".css": true, ".js": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".svg": true, ".woff2": true, ".woff": true, ".json": true}

var rootFiles = map[string]bool{"plugin.json": true, "plugin.wasm": true, "README.md": true, "README.txt": true,
	"LICENSE": true, "LICENSE.txt": true, "LICENSE.md": true, "CHANGELOG.md": true}

var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// ReadZip validates a plugin package and loads it into memory.
func ReadZip(data []byte, source string) (*Package, error) {
	if len(data) > MaxZipBytes {
		return nil, fmt.Errorf("the package is larger than %d MB", MaxZipBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("not a valid zip file")
	}
	if len(zr.File) > maxEntries {
		return nil, errors.New("the package has too many files")
	}
	sum := sha256.Sum256(data)
	p := &Package{SHA256: hex.EncodeToString(sum[:]), Source: source, files: map[string][]byte{}}
	var manifest []byte
	var total int64
	lower := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		if !cleanPath(name) || strings.Contains(name, `\`) || strings.ContainsAny(name, "\x00:*?\"<>|") {
			return nil, fmt.Errorf("invalid file name %q", name)
		}
		if f.Mode()&fs.ModeType != 0 { // symlinks, devices …
			return nil, fmt.Errorf("%s is not a regular file", name)
		}
		if lower[strings.ToLower(name)] {
			return nil, fmt.Errorf("duplicate file %q", name)
		}
		lower[strings.ToLower(name)] = true
		dir, _, _ := strings.Cut(name, "/")
		ext := strings.ToLower(path.Ext(name))
		switch {
		case !strings.Contains(name, "/"):
			if !rootFiles[name] {
				return nil, fmt.Errorf("unexpected file %q", name)
			}
		case dir == "assets" && assetExt[ext], dir == "admin" && (assetExt[ext] || ext == ".html"):
		case dir == "templates":
			if !reTemplate.MatchString(name) {
				return nil, fmt.Errorf("template %q: templates/ holds .html files (a-z, 0-9, - and _), no folders", name)
			}
			if f.UncompressedSize64 > maxTemplateBytes {
				return nil, fmt.Errorf("template %s is larger than 64 KB", name)
			}
		default:
			return nil, fmt.Errorf("file %q is not allowed (assets/ and admin/ hold css, js, images, fonts and json; admin/ also html; templates/ html)", name)
		}
		if f.UncompressedSize64 > maxFileBytes {
			return nil, fmt.Errorf("%s is too large", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		// The sizes in the zip directory can lie: count what is read.
		b, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(b) > maxFileBytes {
			return nil, fmt.Errorf("%s is too large", name)
		}
		if total += int64(len(b)); total > maxUnpackedBytes {
			return nil, errors.New("the package is too large when unpacked")
		}
		switch {
		case name == "plugin.json":
			manifest = b
		case name == "plugin.wasm":
			p.Wasm = b
		case dir == "assets" || dir == "admin" || dir == "templates":
			p.files[name] = b
		}
	}
	if manifest == nil {
		return nil, errors.New("plugin.json is missing")
	}
	if !bytes.HasPrefix(p.Wasm, wasmMagic) {
		return nil, errors.New("plugin.wasm is missing or not a WebAssembly module")
	}
	if p.Manifest, err = ParseManifest(manifest); err != nil {
		return nil, err
	}
	if err := checkTemplates(p.files); err != nil {
		return nil, err
	}
	for _, a := range p.Manifest.Permissions.FrontendAssets {
		if _, ok := p.files[a]; !ok {
			return nil, fmt.Errorf("frontend asset %s is missing", a)
		}
	}
	for _, pg := range p.Manifest.Admin {
		if pg.Kind == "iframe" {
			if _, ok := p.files[pg.File]; !ok {
				return nil, fmt.Errorf("admin page file %s is missing", pg.File)
			}
		}
	}
	return p, nil
}
