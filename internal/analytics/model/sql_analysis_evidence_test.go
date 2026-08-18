package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSQLAnalysisEvidenceRoundTripsEmptyAndNonEmptyLineage(t *testing.T) {
	for _, evidence := range []*SQLAnalysisEvidence{
		{Validated: true},
		{Validated: true, SourceRefs: []string{"orders"}, ModelRefs: []string{"daily"}},
	} {
		encoded, err := json.Marshal(Table{SQLAnalysisEvidence: evidence})
		if err != nil {
			t.Fatal(err)
		}
		var decoded Table
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.SQLAnalysisEvidence == nil || !reflect.DeepEqual(decoded.SQLAnalysisEvidence, evidence) {
			t.Fatalf("evidence = %#v, want %#v", decoded.SQLAnalysisEvidence, evidence)
		}
	}
}
