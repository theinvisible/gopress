package render

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// Markdown for content written by AI agents (MCP interface): GitHub
// flavored Markdown (tables, strikethrough, autolinks, task lists). Raw
// HTML passes through here; callers sanitize the result like any other
// post content.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// MarkdownToHTML converts Markdown to (unsanitized) HTML.
func MarkdownToHTML(src string) (string, error) {
	var b bytes.Buffer
	if err := markdown.Convert([]byte(src), &b); err != nil {
		return "", err
	}
	return b.String(), nil
}
