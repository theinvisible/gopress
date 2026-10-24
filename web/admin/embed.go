// Package webadmin embeds the admin templates and static files.
package webadmin

import "embed"

// FS holds templates/ and static/.
//
//go:embed all:templates all:static
var FS embed.FS
