package testshard

import (
	"reflect"
	"regexp"
	"testing"
)

func TestParseListReturnsOnlyTopLevelTests(t *testing.T) {
	output := `TestZulu
BenchmarkIgnored
TestAlpha
ExampleDemo
FuzzDecode
ok  	github.com/flidai/leapview/internal/app	0.123s
`

	if got, want := ParseList(output), []string{"TestZulu", "TestAlpha", "ExampleDemo", "FuzzDecode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseList() = %v, want %v", got, want)
	}
}

func TestSelectSortsAndBalancesTestsAcrossShards(t *testing.T) {
	tests := []string{"TestGolf", "TestBravo", "TestEcho", "TestAlpha", "TestFoxtrot", "TestCharlie", "TestDelta"}
	wants := [][]string{
		{"TestAlpha", "TestDelta", "TestGolf"},
		{"TestBravo", "TestEcho"},
		{"TestCharlie", "TestFoxtrot"},
	}

	seen := map[string]int{}
	for index, want := range wants {
		got, err := Select(tests, index, len(wants))
		if err != nil {
			t.Fatalf("Select(%d) error: %v", index, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Select(%d) = %v, want %v", index, got, want)
		}
		for _, name := range got {
			seen[name]++
		}
	}
	for _, name := range tests {
		if seen[name] != 1 {
			t.Fatalf("%s selected %d times, want exactly once", name, seen[name])
		}
	}
}

func TestSelectRejectsInvalidShardConfiguration(t *testing.T) {
	for _, test := range []struct {
		index int
		total int
	}{
		{index: 0, total: 0},
		{index: -1, total: 2},
		{index: 2, total: 2},
	} {
		if _, err := Select([]string{"TestOne"}, test.index, test.total); err == nil {
			t.Fatalf("Select(index=%d, total=%d) succeeded, want error", test.index, test.total)
		}
	}
}

func TestPatternMatchesOnlySelectedTopLevelTests(t *testing.T) {
	pattern, err := Pattern([]string{"TestAlpha", "TestName_WithUnderscore"})
	if err != nil {
		t.Fatalf("Pattern() error: %v", err)
	}
	expression := regexp.MustCompile(pattern)
	for _, name := range []string{"TestAlpha", "TestName_WithUnderscore"} {
		if !expression.MatchString(name) {
			t.Fatalf("pattern %q does not match %q", pattern, name)
		}
	}
	for _, name := range []string{"Test", "TestAlphaSuffix", "PrefixTestAlpha"} {
		if expression.MatchString(name) {
			t.Fatalf("pattern %q unexpectedly matches %q", pattern, name)
		}
	}
}

func TestPatternRejectsEmptySelection(t *testing.T) {
	if _, err := Pattern(nil); err == nil {
		t.Fatal("Pattern(nil) succeeded, want error")
	}
}
