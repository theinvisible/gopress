package db

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenMigratesIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	for range 2 {
		d, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		files, _ := fs.Glob(migrations, "migrations/*.sql")
		var n int
		if err := d.R.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(files) {
			t.Fatalf("schema_migrations = %d, %v; want %d", n, err, len(files))
		}
		d.Close()
	}
}

func TestPostRoundTripAndQueries(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	at := func(s string) Time { tm, _ := time.Parse(TimeLayout, s); return Time{tm} }

	for _, p := range []Post{
		{ID: 10, Type: TypePost, Status: StatusPublish, Slug: "a", Title: "Alpha", Content: "<p>Hello world</p>", Date: at("2024-01-05 10:00:00"), DateGMT: at("2024-01-05 09:00:00")},
		{ID: 11, Type: TypePost, Status: StatusPublish, Slug: "b", Title: "Beta", Content: "x", Date: at("2024-02-01 00:30:00"), DateGMT: at("2024-01-31 23:30:00")},
		{ID: 12, Type: TypePost, Status: StatusDraft, Slug: "c", Title: "Draft", Date: at("2024-03-01 00:00:00"), DateGMT: at("2024-03-01 00:00:00")},
	} {
		p := p
		if err := InsertPost(ctx, d.W, &p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := GetPost(ctx, d.R, 10)
	if err != nil || got.Title != "Alpha" || got.Date.Format(TimeLayout) != "2024-01-05 10:00:00" {
		t.Fatalf("GetPost = %+v, %v", got, err)
	}

	posts, total, err := ListPosts(ctx, d.R, PostQuery{Type: TypePost, Limit: 10})
	if err != nil || total != 2 || posts[0].ID != 11 {
		t.Fatalf("ListPosts = %v total %d, %v", posts, total, err)
	}
	// Month filter uses the local date: post 11 is February locally.
	posts, _, _ = ListPosts(ctx, d.R, PostQuery{Type: TypePost, Year: 2024, Month: 2})
	if len(posts) != 1 || posts[0].ID != 11 {
		t.Fatalf("month filter = %v", posts)
	}
	prev, err := AdjacentPost(ctx, d.R, &posts[0], true)
	if err != nil || prev == nil || prev.ID != 10 {
		t.Fatalf("AdjacentPost = %v, %v", prev, err)
	}

	ids, n, err := Search(ctx, d.R, `hello the`, 10, 0)
	if err != nil || n != 1 || ids[0] != 10 {
		t.Fatalf("Search = %v %d, %v", ids, n, err)
	}
	if ids, n, _ := Search(ctx, d.R, `100%`, 10, 0); n != 0 || len(ids) != 0 {
		t.Fatalf("Search(100%%) = %v %d, want no match (LIKE escaping)", ids, n)
	}
}

func TestOptions(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	if err := SetOption(ctx, d.W, "posts_per_page", 10); err != nil {
		t.Fatal(err)
	}
	var n int
	ok, err := GetOption(ctx, d.R, "posts_per_page", &n)
	if !ok || err != nil || n != 10 {
		t.Fatalf("GetOption = %d %v %v", n, ok, err)
	}
	ok, err = GetOption(ctx, d.R, "missing", &n)
	if ok || err != nil {
		t.Fatalf("missing option = %v %v", ok, err)
	}
}

func TestSearchTerms(t *testing.T) {
	got := SearchTerms(`"exact phrase" the router, a ubuntu+deb`)
	want := []string{"exact phrase", "router", "ubuntu", "deb"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("SearchTerms = %q, want %q", got, want)
	}
	if got := SearchTerms("the a"); len(got) != 1 || got[0] != "the a" {
		t.Errorf("SearchTerms(stopwords) = %q", got)
	}
}
