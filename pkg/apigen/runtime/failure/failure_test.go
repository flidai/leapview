package failure

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchPreservesErrorsIsAndResolvesGeneratedContract(t *testing.T) {
	sentinel := New("not_found", "widget not found")
	err := fmt.Errorf("load widget: %w", sentinel)
	require.ErrorIs(t, err, sentinel)

	contract, ok := Match([]Contract{{
		Kind: "not_found", StatusCode: 404, Code: "WIDGET_NOT_FOUND", PublicDetail: "Widget not found.",
	}}, err)
	require.True(t, ok)
	require.Equal(t, "WIDGET_NOT_FOUND", contract.Code)
}

func TestWrapPreservesCause(t *testing.T) {
	cause := errors.New("storage unavailable")
	err := Wrap("unavailable", cause)
	require.ErrorIs(t, err, cause)
	require.Equal(t, "unavailable", err.(*KindError).APIGenFailureKind())
}

func TestValidateContractsRejectsAmbiguity(t *testing.T) {
	err := ValidateContracts([]Contract{
		{Kind: "conflict", StatusCode: 409, Code: "CONFLICT", PublicDetail: "Conflict."},
		{Kind: "conflict", StatusCode: 422, Code: "OTHER", PublicDetail: "Other."},
	})
	require.ErrorContains(t, err, "duplicate failure kind")
}
