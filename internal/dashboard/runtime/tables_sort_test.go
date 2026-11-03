package runtime

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
)

func TestSortAggregateTableRowsPreservesOrderingSemantics(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		values    []any
		want      []any
	}{
		{
			name:   "numeric values and numeric strings sort numerically",
			values: []any{"10", 2, 1.5, "1", int64(3)},
			want:   []any{"1", 1.5, 2, int64(3), "10"},
		},
		{
			name:   "non-numeric values sort case-insensitively",
			values: []any{"beta", "Alpha", "charlie", "alpha"},
			want:   []any{"Alpha", "alpha", "beta", "charlie"},
		},
		{
			name:   "text order orders mixed numeric and non-numeric values",
			values: []any{5, "abc", "10", " 2"},
			want:   []any{" 2", 5, "10", "abc"},
		},
		{
			name:      "descending reverses the comparison",
			direction: "desc",
			values:    []any{"10", "2", "1"},
			want:      []any{"10", "2", "1"},
		},
		{
			name:   "nil sorts with text",
			values: []any{"b", nil, "a"},
			want:   []any{nil, "a", "b"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := make([]map[string]any, len(test.values))
			for index, value := range test.values {
				rows[index] = map[string]any{"sort": value}
			}
			sortAggregateTableRows(rows, dashboard.TableSort{Key: "sort", Direction: test.direction})
			got := make([]any, len(rows))
			for index, row := range rows {
				got[index] = row["sort"]
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("sorted values = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSortAggregateTableRowsKeepsEqualRowsStable(t *testing.T) {
	rows := []map[string]any{
		{"sort": "a", "id": 1},
		{"sort": "A", "id": 2},
		{"sort": "a", "id": 3},
	}
	sortAggregateTableRows(rows, dashboard.TableSort{Key: "sort", Direction: "desc"})
	got := make([]any, len(rows))
	for index, row := range rows {
		got[index] = row["id"]
	}
	if want := []any{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stable order = %#v, want %#v", got, want)
	}
}
