package site

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"strings"
)

//go:embed assets
var coreAssets embed.FS

// coreAssetPath returns the versioned URL path of a core asset.
func coreAssetPath(name string) string {
	data, err := coreAssets.ReadFile("assets/" + name)
	if err != nil {
		return "/static/_core/missing/" + name
	}
	sum := sha256.Sum256(data)
	return "/static/_core/" + hex.EncodeToString(sum[:4]) + "/" + name
}

// serveCoreAsset serves /static/_core/<hash>/<name>.
func serveCoreAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/static/_core/")
	_, name, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	data, err := coreAssets.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if coreAssetPath(name) == r.URL.Path {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	ctype := "application/octet-stream"
	if strings.HasSuffix(name, ".js") {
		ctype = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ctype)
	w.Write(data)
}
