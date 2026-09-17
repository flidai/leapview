package app

import (
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

func TestAPIGenOperationCoverageMatrixIsMechanicallyChecked(t *testing.T) {
	matrix := accessmodule.BuildAPIGenOperationCoverage(accessAPIGenOperationContracts())
	if err := accessmodule.ValidateAPIGenOperationCoverage(matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Operations) != expectedAPIGenAggregateOperationCount {
		t.Fatalf("operation coverage rows = %d, want %d", len(matrix.Operations), expectedAPIGenAggregateOperationCount)
	}
	for _, row := range matrix.Operations {
		if row.SupportStatus == accessmodule.APIGenOperationUnsupported && row.Reason == "" {
			t.Errorf("unsupported operation %q has no reason", row.OperationID)
		}
	}
}
