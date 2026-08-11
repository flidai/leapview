package control

import (
	"errors"
	"fmt"
	"testing"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

func TestDomainSentinelsExposeStableFailureKinds(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		kind string
	}{
		{name: "invalid", err: ErrInvalid, kind: "invalid"},
		{name: "not found", err: ErrNotFound, kind: "not_found"},
		{name: "conflict", err: ErrConflict, kind: "conflict"},
		{name: "incomplete", err: ErrIncomplete, kind: "incomplete"},
		{name: "expired", err: ErrExpired, kind: "expired"},
		{name: "integrity", err: ErrIntegrity, kind: "integrity"},
		{name: "backend", err: ErrBackend, kind: "backend"},
		{name: "internal", err: ErrInternal, kind: "internal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, ok := apigenfailure.KindOf(test.err)
			if !ok || kind != test.kind {
				t.Fatalf("failure kind = %q, %v; want %q", kind, ok, test.kind)
			}
			wrapped := fmt.Errorf("operation failed: %w", test.err)
			if !errors.Is(wrapped, test.err) {
				t.Fatalf("wrapped error does not preserve sentinel: %v", wrapped)
			}
		})
	}
}
