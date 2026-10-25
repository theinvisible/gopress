package db

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ExcerptSourceKey is the post meta that holds what WordPress builds the
// automatic excerpt of imported block content from: excerpt_remove_blocks()
// drops lists of list items, code, images and other blocks, which the
// rendered HTML no longer shows.
const ExcerptSourceKey = "_gopress_excerpt_source"

// ExcerptMeta is the stored form of an excerpt source: it only applies
// while the post content is the one it was made for.
func ExcerptMeta(content, source string) string {
	return contentHash(content) + "\n" + source
}

// ExcerptFromMeta returns the excerpt source if meta belongs to content.
func ExcerptFromMeta(meta, content string) (string, bool) {
	hash, source, ok := strings.Cut(meta, "\n")
	if !ok || hash != contentHash(content) {
		return "", false
	}
	return source, true
}

func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
