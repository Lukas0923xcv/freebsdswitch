// Package textdiff produces unified line diffs (for config comparisons).
package textdiff

import (
	"fmt"
	"strings"
)

// Line is one diff line. Op is ' ', '+' or '-'.
type Line struct {
	Op   byte   `json:"op"`
	Text string `json:"text"`
}

// Lines computes an LCS based line diff.
func Lines(a, b string) []Line {
	x := split(a)
	y := split(b)
	n, m := len(x), len(y)
	// lcs[i][j] = LCS length of x[i:], y[j:]
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []Line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			out = append(out, Line{' ', x[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, Line{'-', x[i]})
			i++
		default:
			out = append(out, Line{'+', y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, Line{'-', x[i]})
	}
	for ; j < m; j++ {
		out = append(out, Line{'+', y[j]})
	}
	return out
}

func split(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Changed reports whether there is any difference.
func Changed(lines []Line) bool {
	for _, l := range lines {
		if l.Op != ' ' {
			return true
		}
	}
	return false
}

// Unified renders a unified diff with the given context.
func Unified(aName, bName, a, b string, context int) string {
	lines := Lines(a, b)
	if !Changed(lines) {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	// collect hunks
	type hunk struct{ start, end int }
	var hunks []hunk
	for i, l := range lines {
		if l.Op == ' ' {
			continue
		}
		s, e := max(0, i-context), min(len(lines), i+context+1)
		if len(hunks) > 0 && s <= hunks[len(hunks)-1].end {
			hunks[len(hunks)-1].end = e
		} else {
			hunks = append(hunks, hunk{s, e})
		}
	}
	for _, h := range hunks {
		ai, bi := 1, 1
		for _, l := range lines[:h.start] {
			if l.Op != '+' {
				ai++
			}
			if l.Op != '-' {
				bi++
			}
		}
		ac, bc := 0, 0
		for _, l := range lines[h.start:h.end] {
			if l.Op != '+' {
				ac++
			}
			if l.Op != '-' {
				bc++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", ai, ac, bi, bc)
		for _, l := range lines[h.start:h.end] {
			sb.WriteByte(l.Op)
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
