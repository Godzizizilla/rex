package daemon

import (
	"fmt"
	"strings"
)

const (
	diffContext  = 3
	diffMaxCells = 4_000_000 // lines(old) * lines(new) limit for the LCS table
)

// unifiedDiff returns a unified diff between two texts, computed with a
// longest-common-subsequence table (fine for the config-sized files that get
// uploaded; larger inputs only get a summary).
func unifiedDiff(oldName, newName, a, b string) string {
	x, y := splitLines(a), splitLines(b)
	if len(x)*len(y) > diffMaxCells {
		return fmt.Sprintf("(too large to diff: %d -> %d lines)", len(x), len(y))
	}

	// lcs[i][j] = length of the LCS of x[i:] and y[j:]
	lcs := make([][]int32, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	type op struct {
		kind byte // ' ', '-', '+'
		text string
		oi   int // old line number (1-based) for ' ' and '-'
		ni   int // new line number for ' ' and '+'
	}
	var ops []op
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			ops = append(ops, op{' ', x[i], i + 1, j + 1})
			i++
			j++
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', x[i], i + 1, j})
			i++
		default:
			ops = append(ops, op{'+', y[j], i, j + 1})
			j++
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		// Grow a hunk around consecutive changes separated by little context.
		start := max(k-diffContext, 0)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(end+diffContext, len(ops))
				break
			}
			end = run
		}
		var oldStart, newStart, oldLen, newLen int
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				if oldLen == 0 {
					oldStart = o.oi
				}
				oldLen++
			}
			if o.kind != '-' {
				if newLen == 0 {
					newStart = o.ni
				}
				newLen++
			}
		}
		if oldLen == 0 {
			oldStart = ops[start].oi
		}
		if newLen == 0 {
			newStart = ops[start].ni
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldLen, newStart, newLen)
		for _, o := range ops[start:end] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.text)
			sb.WriteByte('\n')
		}
		k = end
	}
	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
