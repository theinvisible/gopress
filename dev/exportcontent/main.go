// Command exportcontent writes the editor HTML of all posts and pages of a
// GoPress database as JSON, for the editor round-trip test
// (web/editor/test/roundtrip.mjs).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
)

func main() {
	path := flag.String("db", "local/data/gopress.db", "GoPress database")
	flag.Parse()
	ctx := context.Background()
	d, err := db.Open(ctx, *path)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	var out []map[string]any
	for _, typ := range []string{db.TypePost, db.TypePage} {
		posts, _, err := db.AdminPosts(ctx, d.R, db.AdminPostQuery{Type: typ})
		if err != nil {
			log.Fatal(err)
		}
		for i := range posts {
			p := &posts[i]
			out = append(out, map[string]any{"id": p.ID, "title": p.Title, "format": p.ContentFormat, "content": content.EditorContent(p)})
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(out); err != nil {
		log.Fatal(err)
	}
}
