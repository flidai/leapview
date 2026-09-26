package app

import (
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

func TestAPIGenOperationCoverageMatrixIsMechanicallyChecked(t *testing.T) {
	contracts := accessAPIGenOperationContracts()
	matrix := accessmodule.BuildAPIGenOperationCoverage(contracts)
	if err := accessmodule.ValidateAPIGenOperationCoverage(matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Operations) != expectedAPIGenAggregateOperationCount {
		t.Fatalf("operation coverage rows = %d, want %d", len(matrix.Operations), expectedAPIGenAggregateOperationCount)
	}
	for _, row := range matrix.Operations {
		if contract := contracts[row.OperationID]; contract.AuthzMode == "privilege" && (row.TypedAction == "" || row.Resolver == "") {
			t.Errorf("privilege-protected operation %q lacks an exact typed action and resolver", row.OperationID)
		}
		if row.SupportStatus == accessmodule.APIGenOperationUnsupported && row.Reason == "" {
			t.Errorf("unsupported operation %q has no reason", row.OperationID)
		}
	}
}
