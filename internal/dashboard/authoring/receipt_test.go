package authoring

import (
	"encoding/json"
	"testing"
)

func TestResourceCreateReceiptIsBounded(t *testing.T) {
	receipt := ResourceCreateReceipt{ID: "dashboard:sales", Status: "draft"}
	payload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("receipt fields = %#v, want only id and status", fields)
	}
	for _, field := range []string{"id", "status"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("receipt missing %q: %#v", field, fields)
		}
	}
}
