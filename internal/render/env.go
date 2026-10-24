package render

import (
	"context"

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

	galleryInstance int
	contentImages   int // images seen by FilterContentTags on this page
	fetchPriority   bool
}
