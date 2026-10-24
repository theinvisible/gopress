package site

import (
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// collator orders titles and names like MySQL's utf8mb4_unicode_ci, which
// WordPress uses for page menus, category lists and tag clouds.
var (
	collatorMu sync.Mutex
	collator   = collate.New(language.Und, collate.IgnoreCase)
)

// lessText reports whether a sorts before b.
func lessText(a, b string) bool {
	collatorMu.Lock() // collate.Collator is not safe for concurrent use
	defer collatorMu.Unlock()
	return collator.CompareString(a, b) < 0
}
