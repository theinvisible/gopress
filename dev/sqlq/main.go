// Command sqlq runs read-only SQL against a GoPress database and prints
// tab-separated rows. Dev tool: go run ./dev/sqlq local/data/gopress.db "SELECT ..."
package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: sqlq <db> <sql>...")
		os.Exit(2)
	}
	d, err := sql.Open("sqlite", "file:"+os.Args[1]+"?mode=ro")
	if err != nil {
		fail(err)
	}
	defer d.Close()
	for _, q := range os.Args[2:] {
		rows, err := d.Query(q)
		if err != nil {
			fail(err)
		}
		cols, _ := rows.Columns()
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				fail(err)
			}
			out := make([]string, len(vals))
			for i, v := range vals {
				out[i] = v.String
			}
			fmt.Println(strings.Join(out, "\t"))
		}
		if err := rows.Err(); err != nil {
			fail(err)
		}
		rows.Close()
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
