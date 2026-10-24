package db

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSQLIsStatic guards against SQL injection: in every SQL call of the
// module (ExecContext, QueryContext, QueryRowContext and the query*
// helpers) the query text must be built only from string literals,
// constants, placeholders and other such fragments. Values always go
// through "?" parameters; formatting (fmt.Sprintf, strconv) or any other
// expression in a query fails this test.
func TestSQLIsStatic(t *testing.T) {
	root, _ := filepath.Abs("../..")
	fset := token.NewFileSet()
	checked := 0
	var files []*ast.File
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "local", ".git", "web", "dev":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		return nil
	})
	funcs := map[string][]*ast.FuncDecl{}
	for _, f := range files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[fn.Name.Name] = append(funcs[fn.Name.Name], fn)
			}
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			g := newGuard(fn, funcs)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSQLCall(call) {
					return true
				}
				// (ctx, query, args…) for database/sql, (ctx, q, query, args…)
				// for the helpers.
				i := 1
				if id, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, "query") {
					i = 2
				}
				if len(call.Args) <= i || fn.Name.Name == "migrate" { // migrations are embedded files
					return true
				}
				checked++
				if !g.static(call.Args[i], 0) {
					t.Errorf("%s: SQL query is not built from static fragments", fset.Position(call.Pos()))
				}
				return true
			})
		}
	}
	if checked < 50 {
		t.Fatalf("only %d SQL calls found; is the walk broken?", checked)
	}
}

func isSQLCall(call *ast.CallExpr) bool {
	var name string
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		name = f.Sel.Name
	case *ast.Ident:
		name = f.Name
	}
	switch name {
	case "ExecContext", "QueryContext", "QueryRowContext", "PrepareContext":
		return true
	}
	// Package-internal helpers such as queryPosts(ctx, q, query, args...).
	return strings.HasPrefix(name, "query") && len(name) > 5 && name[5] >= 'A' && name[5] <= 'Z'
}

// guard checks expressions within one function.
type guard struct {
	fn      *ast.FuncDecl
	funcs   map[string][]*ast.FuncDecl // all functions of the module by name
	assigns map[string][]ast.Expr // local variable → assigned expressions
	params  map[string]bool
}

func newGuard(fn *ast.FuncDecl, funcs map[string][]*ast.FuncDecl) *guard {
	g := &guard{fn: fn, funcs: funcs, assigns: map[string][]ast.Expr{}, params: map[string]bool{}}
	for _, f := range fn.Type.Params.List {
		for _, n := range f.Names {
			g.params[n.Name] = true
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, l := range s.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok {
					continue
				}
				if len(s.Rhs) == len(s.Lhs) {
					g.assigns[id.Name] = append(g.assigns[id.Name], s.Rhs[i])
				} else if i == 0 && len(s.Rhs) == 1 {
					// where, args := q.where(): the first result.
					g.assigns[id.Name] = append(g.assigns[id.Name], s.Rhs[0])
				} else {
					g.assigns[id.Name] = append(g.assigns[id.Name], &ast.BadExpr{})
				}
			}
		case *ast.ValueSpec:
			for i, id := range s.Names {
				if i < len(s.Values) {
					g.assigns[id.Name] = append(g.assigns[id.Name], s.Values[i])
				}
			}
		case *ast.RangeStmt:
			// for _, t := range []string{"a", "b"}: t is as static as the list.
			if id, ok := s.Value.(*ast.Ident); ok {
				g.assigns[id.Name] = append(g.assigns[id.Name], s.X)
			}
		}
		return true
	})
	return g
}

// static reports whether e can only produce fixed SQL text.
func (g *guard) static(e ast.Expr, depth int) bool {
	if depth > 12 {
		return true // cycles (query += …) were checked on the way in
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Kind == token.STRING
	case *ast.ParenExpr:
		return g.static(x.X, depth+1)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && g.static(x.X, depth+1) && g.static(x.Y, depth+1)
	case *ast.IndexExpr:
		return g.static(x.X, depth+1)
	case *ast.CompositeLit:
		for _, el := range x.Elts {
			if !g.static(el, depth+1) {
				return false
			}
		}
		return true
	case *ast.Ident:
		if exprs, ok := g.assigns[x.Name]; ok {
			for _, a := range exprs {
				if !g.static(a, depth+1) {
					return false
				}
			}
			return true
		}
		if g.params[x.Name] {
			// Only the query helpers may pass a query parameter on; their
			// callers are checked.
			n := g.fn.Name.Name
			return isSQLCall(&ast.CallExpr{Fun: ast.NewIdent(n)}) || n == "Tx"
		}
		return true // package-level constant (e.g. postColumns)
	case *ast.CallExpr:
		switch f := x.Fun.(type) {
		case *ast.Ident:
			switch f.Name {
			case "placeholders":
				return true
			case "append":
				for _, a := range x.Args {
					if !g.static(a, depth+1) {
						return false
					}
				}
				return true
			}
		case *ast.SelectorExpr:
			if pkg, ok := f.X.(*ast.Ident); ok && pkg.Name == "strings" && (f.Sel.Name == "Join" || f.Sel.Name == "Repeat") {
				return g.static(x.Args[0], depth+1)
			}
			if f.Sel.Name == "table" { // importer: validated table prefix
				return true
			}
			// A method of the module (e.g. AdminPostQuery.where) whose
			// first result is static.
			if fns := g.funcs[f.Sel.Name]; len(fns) > 0 && f.Sel.Name[0] >= 'a' && f.Sel.Name[0] <= 'z' {
				for _, fn := range fns {
					if !returnsStatic(fn, g.funcs, depth) {
						return false
					}
				}
				return true
			}
		}
	}
	return false
}

// returnsStatic reports whether every return statement of fn has a
// static first result.
func returnsStatic(fn *ast.FuncDecl, funcs map[string][]*ast.FuncDecl, depth int) bool {
	g := newGuard(fn, funcs)
	ok := true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, lit := n.(*ast.FuncLit); lit {
			return false
		}
		if r, isRet := n.(*ast.ReturnStmt); isRet && len(r.Results) > 0 && !g.static(r.Results[0], depth+1) {
			ok = false
		}
		return true
	})
	return ok
}

// The guard itself: unsafe query construction must be detected.
func TestSQLGuardCatches(t *testing.T) {
	src := `package x
func ok(ctx context.Context, q Querier, ids []int64) {
	where := "a = ?"
	if len(ids) > 0 {
		where += " AND id IN (" + placeholders(len(ids)) + ")"
	}
	q.QueryContext(ctx, "SELECT x FROM t WHERE "+where, 1)
}
func bad1(ctx context.Context, q Querier, name string) { q.ExecContext(ctx, "DELETE FROM t WHERE n = '"+name+"'") }
func bad2(ctx context.Context, q Querier, n int) { q.QueryContext(ctx, fmt.Sprintf("SELECT * FROM t LIMIT %d", n)) }
func bad3(ctx context.Context, q Querier, r *http.Request) {
	order := r.FormValue("order")
	q.QueryContext(ctx, "SELECT * FROM t ORDER BY "+order)
}
func bad4(ctx context.Context, q Querier, s string) { queryPosts(ctx, q, "SELECT * FROM posts WHERE slug = '"+s+"'") }
`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn := decl.(*ast.FuncDecl)
		g := newGuard(fn, nil)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSQLCall(call) {
				return true
			}
			i := 1
			if id, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, "query") {
				i = 2
			}
			if got, want := g.static(call.Args[i], 0), fn.Name.Name == "ok"; got != want {
				t.Errorf("%s: static = %v, want %v", fn.Name.Name, got, want)
			}
			return true
		})
	}
}

// Hostile input is data, never SQL.
func TestInjectionInputs(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	now := Time{time.Now().UTC()}
	InsertPost(ctx, d.W, &Post{ID: 1, Type: TypePost, Status: StatusPublish, Slug: "a", Title: "Alpha", Date: now, DateGMT: now})
	InsertPost(ctx, d.W, &Post{ID: 2, Type: TypePost, Status: StatusDraft, Slug: "b", Title: "Beta", Date: now, DateGMT: now})
	for _, in := range []string{`' OR 1=1 --`, `"; DROP TABLE posts; --`, `%`, `_`, `\`, `a' UNION SELECT * FROM users --`} {
		if ps, n, err := AdminPosts(ctx, d.R, AdminPostQuery{Type: TypePost, Search: in, OrderBy: in, Month: "2024' OR 1=1", Limit: 20}); err != nil || n != 0 || len(ps) != 0 {
			t.Errorf("AdminPosts search %q: %d posts, %v", in, n, err)
		}
		if cs, n, err := AdminComments(ctx, d.R, AdminCommentQuery{Search: in, Limit: 20}); err != nil || n != 0 || len(cs) != 0 {
			t.Errorf("AdminComments search %q: %d, %v", in, n, err)
		}
		if _, err := UserByLogin(ctx, d.R, in); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("UserByLogin(%q): %v", in, err)
		}
		if _, n, err := Search(ctx, d.R, in, 10, 0); err != nil || n != 0 {
			t.Errorf("Search(%q): %d, %v", in, n, err)
		}
	}
	// A NUL byte is dropped (SQLite would cut the pattern there): the same
	// as an empty search.
	if _, n, _ := AdminPosts(ctx, d.R, AdminPostQuery{Type: TypePost, Search: "x\x00"}); n != 0 {
		t.Errorf("search with NUL matched %d posts", n)
	}
	var n int
	d.R.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n)
	if n != 2 {
		t.Errorf("posts = %d after hostile input", n)
	}
}
