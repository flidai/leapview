package ci

import (
	"slices"
	"sort"
	"testing"
)

func TestUnionStringsPreservesSetSemanticsAndInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		current   []string
		additions []string
		want      []string
	}{
		{name: "nil inputs"},
		{name: "duplicates", current: []string{"core", "core"}, additions: []string{"site", "site"}, want: []string{"core", "site"}},
		{name: "overlap", current: []string{"core", "reports"}, additions: []string{"reports", "site"}, want: []string{"core", "reports", "site"}},
		{name: "disjoint", current: []string{"core"}, additions: []string{"site"}, want: []string{"core", "site"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			current := slices.Clone(tt.current)
			additions := slices.Clone(tt.additions)
			currentBefore := slices.Clone(current)
			additionsBefore := slices.Clone(additions)

			got := unionStrings(current, additions)
			if !sameStringSet(got, tt.want) {
				t.Fatalf("unionStrings() = %v, want set %v", got, tt.want)
			}
			if !slices.Equal(current, currentBefore) || !slices.Equal(additions, additionsBefore) {
				t.Fatalf("unionStrings mutated inputs: current %v -> %v, additions %v -> %v", currentBefore, current, additionsBefore, additions)
			}
		})
	}
}

func TestUnionStringsPreservesAliasedBackingSlices(t *testing.T) {
	t.Parallel()

	backing := []string{"core", "reports", "site"}
	current := backing[:2]
	additions := backing[1:]
	before := slices.Clone(backing)

	got := unionStrings(current, additions)
	if !sameStringSet(got, []string{"core", "reports", "site"}) {
		t.Fatalf("unionStrings() = %v, want all aliased values", got)
	}
	if !slices.Equal(backing, before) {
		t.Fatalf("unionStrings mutated aliased backing slice: %v -> %v", before, backing)
	}
}

func sameStringSet(got, want []string) bool {
	gotCopy := slices.Clone(got)
	wantCopy := slices.Clone(want)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	return slices.Equal(gotCopy, wantCopy)
}
