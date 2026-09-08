package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestAppendExplorationCommandIDIsStableNativeUUIDv7(t *testing.T) {
	requestID := "01912f14-7b3c-7e31-8a74-6a6e8f9d4c20"
	first, err := appendExplorationCommandID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := appendExplorationCommandID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same request produced different command IDs: %q and %q", first, second)
	}
	parsed, err := uuid.Parse(string(first))
	if err != nil {
		t.Fatalf("append command ID %q is not a UUID: %v", first, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("append command ID version = %d, want UUIDv7", parsed.Version())
	}
	if parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("append command ID variant = %v, want RFC4122", parsed.Variant())
	}
	if other, err := appendExplorationCommandID("01912f14-7b3c-7e32-8a74-6a6e8f9d4c20"); err != nil || other == first {
		t.Fatalf("different request reused command ID %q (err=%v)", first, err)
	}
}

func TestAppendExplorationCommandIDRejectsNonUUIDv7(t *testing.T) {
	for _, requestID := range []string{
		"add-exploration-test-1",
		"01912f14-7b3c-4e31-8a74-6a6e8f9d4c20",
		"01912f14-7b3c-7e31-ca74-6a6e8f9d4c20",
		"01912F14-7B3C-7E31-8A74-6A6E8F9D4C20",
	} {
		if _, err := appendExplorationCommandID(requestID); err == nil {
			t.Fatalf("request ID %q was accepted", requestID)
		}
	}
}
