package render

import (
	"context"
	"slices"

	"github.com/theinvisible/gopress/internal/db"
)

// Linker builds URLs; implemented by the site package.
type Linker interface {
	// PostURL returns the permalink of a post, page or attachment.
	PostURL(p *db.Post) string
	// UploadURL returns the URL of a file below the uploads directory.
	UploadURL(file string) string
}

// Env is the environment of one page render. It is not safe for concurrent
// use; create one per request.
type Env struct {
	Ctx    context.Context
	Q      db.Querier
	Links  Linker
	Post   *db.Post // post whose content is rendered, may be nil
	IsFeed bool
	// SizeLimits are the configured image sizes (name → width, height).
	SizeLimits map[string][2]int

	// Assets are the plugin frontend assets the rendered output needs
	// ("<plugin>/<path>"), included once per page.
	Assets []string

	galleryInstance int
	contentImages   int // images seen by FilterContentTags on this page
	fetchPriority   bool
}

// AddAssets records frontend assets (deduplicated).
func (e *Env) AddAssets(assets ...string) {
	for _, a := range assets {
		if !slices.Contains(e.Assets, a) {
			e.Assets = append(e.Assets, a)
		}
	}
}
