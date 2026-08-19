package duckdbsql

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzRewriteNoPanicAndDeterministic(f *testing.F) {
	f.Add("SELECT source.orders", 7, 20, "orders")
	f.Add("SELECT café", 7, 7, "-- prefix\n")
	f.Add("SELECT 1", 0, 8, "SELECT 2")
	f.Fuzz(func(t *testing.T, sql string, start, end int, replacement string) {
		edit := Edit{Span: Span{Start: start, End: end}, Replacement: replacement}
		got, err := Rewrite(sql, []Edit{edit})
		if err != nil {
			return
		}
		if !utf8.ValidString(got) {
			t.Fatalf("successful rewrite returned invalid UTF-8: %q", got)
		}
		again, err := Rewrite(sql, []Edit{edit})
		if err != nil {
			t.Fatalf("same edit failed on second invocation: %v", err)
		}
		if got != again {
			t.Fatalf("rewrite is not deterministic: first %q, second %q", got, again)
		}
	})
}

func FuzzRewritePreservesUntouchedFragments(f *testing.F) {
	f.Add("SELECT source.orders, source.payments", 7, 20, "orders")
	f.Add("aébc", 0, 1, "x")
	f.Fuzz(func(t *testing.T, sql string, start, end int, replacement string) {
		if !utf8.ValidString(sql) || !utf8.ValidString(replacement) || start < 0 || end < start || end > len(sql) || !utf8Boundary(sql, start) || !utf8Boundary(sql, end) {
			return
		}
		got, err := Rewrite(sql, []Edit{{Span: Span{Start: start, End: end}, Replacement: replacement}})
		if err != nil {
			return
		}
		// With one edit, both untouched fragments must survive byte-for-byte and
		// in order. This catches accidental rune-index or post-edit offset use.
		prefix, suffix := sql[:start], sql[end:]
		prefixIndex := strings.Index(got, prefix)
		if prefix != "" && prefixIndex < 0 {
			t.Fatalf("rewritten SQL %q dropped untouched prefix %q", got, prefix)
		}
		suffixIndex := strings.LastIndex(got, suffix)
		if suffix != "" && suffixIndex < 0 {
			t.Fatalf("rewritten SQL %q dropped untouched suffix %q", got, suffix)
		}
		if prefix != "" && suffix != "" && prefixIndex > suffixIndex {
			t.Fatalf("untouched fragments changed order: prefix at %d, suffix at %d", prefixIndex, suffixIndex)
		}
	})
}
