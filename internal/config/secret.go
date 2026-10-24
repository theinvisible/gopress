package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// LoadOrCreateSecret returns the 32-byte site secret stored hex-encoded at
// path, creating it with mode 0600 on first use. It refuses secret files
// readable by group or others.
func LoadOrCreateSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if _, err := f.WriteString(hex.EncodeToString(secret) + "\n"); err != nil {
			f.Close()
			return nil, err
		}
		return secret, f.Close()
	}
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: permissions %v are too open (want 0600)", path, fi.Mode().Perm())
	}
	secret, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(secret) < 32 {
		return nil, fmt.Errorf("%s: invalid secret", path)
	}
	return secret, nil
}
