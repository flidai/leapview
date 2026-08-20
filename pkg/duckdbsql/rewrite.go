package duckdbsql

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// Edit replaces the half-open byte range Span with Replacement. Spans are
// measured against the original SQL string; they are not adjusted after an
// earlier edit is applied.
//
// Edit intentionally carries only generic source-rewriting information. It
// does not expose any DuckDB AST representation or application policy.
type Edit struct {
	Span        Span
	Replacement string
}

// Rewrite applies edits to sql and returns the rewritten SQL. Edits are
// validated before any bytes are changed. This makes a failed rewrite
// side-effect free and guarantees that all offsets refer to the same input.
//
// The input and replacements must be valid UTF-8, and every span endpoint
// must lie on a UTF-8 rune boundary. Ranges are half-open: [Start, End).
// Distinct zero-width edits are allowed, while duplicate or overlapping edits
// are rejected. The result is deterministic regardless of the caller's edit
// order.
func Rewrite(sql string, edits []Edit) (string, error) {
	if !utf8.ValidString(sql) {
		return "", fmt.Errorf("rewrite: SQL is not valid UTF-8")
	}

	ordered := append([]Edit(nil), edits...)
	for index, edit := range ordered {
		if !utf8.ValidString(edit.Replacement) {
			return "", fmt.Errorf("rewrite: edit %d replacement is not valid UTF-8", index)
		}
		if edit.Span.Start < 0 || edit.Span.End < edit.Span.Start || edit.Span.End > len(sql) {
			return "", fmt.Errorf("rewrite: edit %d span [%d,%d) is out of range for %d-byte SQL", index, edit.Span.Start, edit.Span.End, len(sql))
		}
		if !utf8Boundary(sql, edit.Span.Start) || !utf8Boundary(sql, edit.Span.End) {
			return "", fmt.Errorf("rewrite: edit %d span [%d,%d) does not align to UTF-8 boundaries", index, edit.Span.Start, edit.Span.End)
		}
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Span.Start != ordered[j].Span.Start {
			return ordered[i].Span.Start < ordered[j].Span.Start
		}
		return ordered[i].Span.End < ordered[j].Span.End
	})
	for index := 1; index < len(ordered); index++ {
		previous, current := ordered[index-1].Span, ordered[index].Span
		if previous.Start == current.Start && previous.End == current.End {
			return "", fmt.Errorf("rewrite: duplicate span [%d,%d)", current.Start, current.End)
		}
		if spansOverlap(previous, current) {
			return "", fmt.Errorf("rewrite: overlapping spans [%d,%d) and [%d,%d)", previous.Start, previous.End, current.Start, current.End)
		}
	}

	// Apply from right to left so each edit remains anchored to the original
	// SQL offsets. This also avoids constructing a potentially large sequence
	// of intermediate strings.
	result := sql
	for index := len(ordered) - 1; index >= 0; index-- {
		edit := ordered[index]
		result = result[:edit.Span.Start] + edit.Replacement + result[edit.Span.End:]
	}
	return result, nil
}

func utf8Boundary(value string, offset int) bool {
	return offset == 0 || offset == len(value) || utf8.RuneStart(value[offset])
}

func spansOverlap(left, right Span) bool {
	// Zero-width edits are points. A point inside a non-empty range overlaps;
	// points at either boundary are intentionally allowed and have stable
	// ordering under the sort above.
	if left.Start == left.End {
		return left.Start > right.Start && left.Start < right.End
	}
	if right.Start == right.End {
		return right.Start > left.Start && right.Start < left.End
	}
	return left.Start < right.End && right.Start < left.End
}
