package materialize

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestRuntimeSourceObservationsDeepCopyNullable(t *testing.T) {
	nullable := true
	runtime := &Runtime{sourceObservations: []SourceObservation{{
		ID: "orders", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Nullable: &nullable}},
	}}}

	first := runtime.SourceObservations()
	*first[0].Schema[0].Nullable = false
	second := runtime.SourceObservations()
	if second[0].Schema[0].Nullable == nil || !*second[0].Schema[0].Nullable {
		t.Fatalf("source observation nullable identity was aliased: %#v", second)
	}
}
