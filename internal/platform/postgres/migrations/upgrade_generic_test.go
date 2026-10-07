package migrations

import "testing"

func TestUpgradeBoundaryAllowsCompleteFutureAndPolicyOnlyTransitions(t *testing.T) {
	for _, pair := range [][2]int64{{28, 30}, {30, 31}, {31, 33}, {30, 30}} {
		if err := validateUpgradeBoundary(pair[0], pair[1], statusesThrough(int(pair[0]), int(pair[1]))); err != nil {
			t.Errorf("%v: %v", pair, err)
		}
	}
}
