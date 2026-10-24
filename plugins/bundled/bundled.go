// Package bundled embeds the plugin packages shipped with GoPress (built
// into zips/ by `make plugins`).
package bundled

import (
	"embed"
	"io/fs"
)

//go:embed all:zips
var files embed.FS

// FS returns the bundled plugin packages (*.zip).
func FS() fs.FS {
	sub, err := fs.Sub(files, "zips")
	if err != nil {
		panic(err)
	}
	return sub
}
