package duckdbsql

import (
	"strings"
	"testing"
)

func TestRewriteIsDeterministicAndPreservesUntouchedBytes(t *testing.T) {
	sql := "SELECT café, amount FROM source.orders WHERE note = 'source.orders'"
	first := strings.Index(sql, "café")
	second := strings.Index(sql, "source.orders")
	edits := []Edit{
		{Span: Span{Start: second, End: second + len("source.orders")}, Replacement: "(SELECT * FROM orders.csv)"},
		{Span: Span{Start: first, End: first + len("café")}, Replacement: "customer_name"},
	}
	want := "SELECT customer_name, amount FROM (SELECT * FROM orders.csv) WHERE note = 'source.orders'"

	got, err := Rewrite(sql, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Rewrite() = %q, want %q", got, want)
	}

	// Caller ordering must not affect the output, and Rewrite must not sort the
	// caller's backing slice as a side effect.
	reversed := []Edit{edits[0], edits[1]}
	gotReversed, err := Rewrite(sql, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if gotReversed != got {
		t.Fatalf("reordered edits produced %q, want %q", gotReversed, got)
	}
	if reversed[0] != edits[0] || reversed[1] != edits[1] {
		t.Fatal("Rewrite mutated caller edit order")
	}
}

func TestRewriteAllowsStableBoundaryInsertions(t *testing.T) {
	sql := "SELECT 1"
	got, err := Rewrite(sql, []Edit{
		{Span: Span{Start: len(sql), End: len(sql)}, Replacement: ";"},
		{Span: Span{Start: 0, End: 0}, Replacement: "-- generated\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "-- generated\nSELECT 1;"; got != want {
		t.Fatalf("Rewrite() = %q, want %q", got, want)
	}
}

func TestRewriteRejectsDuplicateAndOverlappingSpans(t *testing.T) {
	tests := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{
			name: "duplicate",
			edits: []Edit{
				{Span: Span{Start: 0, End: 1}, Replacement: "x"},
				{Span: Span{Start: 0, End: 1}, Replacement: "y"},
			},
			want: "duplicate span",
		},
		{
			name: "nested",
			edits: []Edit{
				{Span: Span{Start: 1, End: 5}, Replacement: "x"},
				{Span: Span{Start: 2, End: 3}, Replacement: "y"},
			},
			want: "overlapping spans",
		},
		{
			name: "zero width inside range",
			edits: []Edit{
				{Span: Span{Start: 1, End: 4}, Replacement: "x"},
				{Span: Span{Start: 2, End: 2}, Replacement: "y"},
			},
			want: "overlapping spans",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Rewrite("abcdef", test.edits)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Rewrite() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRewriteRejectsInvalidRangesAndUTF8Boundaries(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		edit Edit
		want string
	}{
		{name: "negative start", sql: "SELECT 1", edit: Edit{Span: Span{Start: -1, End: 1}}, want: "out of range"},
		{name: "reversed", sql: "SELECT 1", edit: Edit{Span: Span{Start: 4, End: 3}}, want: "out of range"},
		{name: "past end", sql: "SELECT 1", edit: Edit{Span: Span{Start: 0, End: 9}}, want: "out of range"},
		{name: "split rune start", sql: "é", edit: Edit{Span: Span{Start: 1, End: 2}}, want: "UTF-8 boundaries"},
		{name: "split rune end", sql: "é", edit: Edit{Span: Span{Start: 0, End: 1}}, want: "UTF-8 boundaries"},
		{name: "invalid SQL", sql: string([]byte{0xff}), edit: Edit{}, want: "valid UTF-8"},
		{name: "invalid replacement", sql: "SELECT 1", edit: Edit{Replacement: string([]byte{0xff})}, want: "replacement is not valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Rewrite(test.sql, []Edit{test.edit})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Rewrite() error = %v, want %q", err, test.want)
			}
		})
	}
}
