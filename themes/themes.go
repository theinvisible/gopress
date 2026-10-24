// Package themes embeds the bundled themes.
package themes

import "embed"

// FS holds one directory per theme with templates/ and static/.
//
//go:embed all:twentytwelve
var FS embed.FS
