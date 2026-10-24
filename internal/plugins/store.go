package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
)

// Store holds the plugin packages: bundled ones (embedded in the binary)
// and uploaded ones (data/plugins/<name>.zip). For each name the newest
// version is used; an upload of the same version replaces a bundled one.
type Store struct {
	root     *os.Root
	log      *slog.Logger
	mu       sync.RWMutex
	bundled  map[string]*Package
	uploaded map[string]*Package
}

// OpenStore loads the packages. Invalid uploaded packages are logged and
// skipped; invalid bundled ones are errors (a broken build).
func OpenStore(dir string, bundled fs.FS, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, log: log, bundled: map[string]*Package{}, uploaded: map[string]*Package{}}
	if bundled != nil {
		files, err := fs.Glob(bundled, "*.zip")
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			b, err := fs.ReadFile(bundled, f)
			if err != nil {
				return nil, err
			}
			p, err := ReadZip(b, "bundled")
			if err != nil {
				return nil, fmt.Errorf("bundled plugin %s: %w", f, err)
			}
			s.bundled[p.Manifest.Name] = p
		}
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".zip")
		if !ok || !e.Type().IsRegular() || !reName.MatchString(name) {
			continue
		}
		b, err := root.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		p, err := ReadZip(b, "uploaded")
		if err == nil && p.Manifest.Name != name {
			err = errors.New("file name does not match the plugin name")
		}
		if err != nil {
			log.Error("plugin package skipped", "file", path.Join(dir, e.Name()), "err", err)
			continue
		}
		s.uploaded[name] = p
	}
	return s, nil
}

// Close releases the store.
func (s *Store) Close() error { return s.root.Close() }

// Get returns the package in use for name.
func (s *Store) Get(name string) *Package {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pick(name)
}

func (s *Store) pick(name string) *Package {
	b, u := s.bundled[name], s.uploaded[name]
	switch {
	case u == nil:
		return b
	case b == nil || CompareVersions(u.Manifest.Version, b.Manifest.Version) >= 0:
		return u
	}
	return b
}

// All returns the packages in use, sorted by name.
func (s *Store) All() []*Package {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := map[string]bool{}
	for n := range s.bundled {
		names[n] = true
	}
	for n := range s.uploaded {
		names[n] = true
	}
	out := make([]*Package, 0, len(names))
	for n := range names {
		out = append(out, s.pick(n))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Name < out[j].Manifest.Name })
	return out
}

// Bundled reports whether a bundled package of name exists.
func (s *Store) Bundled(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bundled[name] != nil
}

// ErrOlder is returned when an upload is older than the bundled version.
var ErrOlder = errors.New("the package is older than the bundled version")

// Install validates and stores an uploaded package.
func (s *Store) Install(data []byte) (*Package, error) {
	p, err := ReadZip(data, "uploaded")
	if err != nil {
		return nil, err
	}
	name := p.Manifest.Name
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.bundled[name]; b != nil && CompareVersions(p.Manifest.Version, b.Manifest.Version) < 0 {
		return nil, ErrOlder
	}
	tmp := name + ".zip.tmp"
	if err := s.root.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := s.root.Rename(tmp, name+".zip"); err != nil {
		s.root.Remove(tmp)
		return nil, err
	}
	s.uploaded[name] = p
	return p, nil
}

// Remove deletes an uploaded package; a bundled one of the same name
// takes its place again.
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploaded[name] == nil {
		return errors.New("only uploaded plugins can be deleted")
	}
	if err := s.root.Remove(name + ".zip"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(s.uploaded, name)
	return nil
}
