package admin

import "strings"

// diffRow is one row of a side-by-side revision comparison.
type diffRow struct {
	Header      string // section name ("Title", "Content") on header rows
	Left, Right string
	Kind        string // "same", "changed", "deleted", "added"
}

// maxDiffLines bounds the quadratic LCS; longer texts are compared as
// a whole block.
const maxDiffLines = 4000

// diffLines compares two texts line by line (longest common subsequence)
// and returns side-by-side rows; unchanged runs longer than a few lines
// are shortened to their context.
func diffLines(name, a, b string) []diffRow {
	rows := []diffRow{{Header: name}}
	if a == b {
		rows = append(rows, diffRow{Left: "", Right: "", Kind: "same-all"})
		return rows
	}
	x, y := splitLines(a), splitLines(b)
	if len(x)*len(y) > maxDiffLines*maxDiffLines/4 || len(x) > maxDiffLines || len(y) > maxDiffLines {
		return append(rows, diffRow{Left: a, Right: b, Kind: "changed"})
	}
	// LCS table, computed from the end.
	n, m := len(x), len(y)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffRow
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			ops = append(ops, diffRow{Left: x[i], Right: y[j], Kind: "same"})
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, diffRow{Right: y[j], Kind: "added"})
			j++
		default:
			ops = append(ops, diffRow{Left: x[i], Kind: "deleted"})
			i++
		}
	}
	// Pair deletions with following additions as changed lines.
	var paired []diffRow
	for k := 0; k < len(ops); {
		if ops[k].Kind != "deleted" {
			paired = append(paired, ops[k])
			k++
			continue
		}
		del := k
		for k < len(ops) && ops[k].Kind == "deleted" {
			k++
		}
		add := k
		for k < len(ops) && ops[k].Kind == "added" {
			k++
		}
		dels, adds := ops[del:add], ops[add:k]
		for p := 0; p < max(len(dels), len(adds)); p++ {
			r := diffRow{Kind: "changed"}
			if p < len(dels) {
				r.Left = dels[p].Left
			} else {
				r.Kind = "added"
			}
			if p < len(adds) {
				r.Right = adds[p].Right
			} else {
				r.Kind = "deleted"
			}
			paired = append(paired, r)
		}
	}
	// Keep 2 lines of context around changes.
	const ctx = 2
	keep := make([]bool, len(paired))
	for k, r := range paired {
		if r.Kind != "same" {
			for d := -ctx; d <= ctx; d++ {
				if k+d >= 0 && k+d < len(paired) {
					keep[k+d] = true
				}
			}
		}
	}
	skipped := false
	for k, r := range paired {
		if keep[k] {
			rows = append(rows, r)
			skipped = false
		} else if !skipped {
			rows = append(rows, diffRow{Kind: "skip"})
			skipped = true
		}
	}
	return rows
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
