// Package uicommand binds browser actions to generated application commands.
package uicommand

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenui "github.com/Yacobolo/toolbelt/apigen/runtime/ui"
)

const HeaderOperationID = "X-LeapView-Operation-ID"

var (
	ErrInvalidBinding    = errors.New("invalid UI command binding")
	ErrOperationMissing  = errors.New("UI command operation identity is missing")
	ErrOperationMismatch = errors.New("UI command operation identity does not match the dispatched command")
)

// Binding is APIGen's generated transport-neutral browser action binding.
type Binding = apigenui.Action

// OperationClaims returns the generated operation identities claimed by a
// browser request. Multiple values are supported only for an explicitly
// composed UI workflow; each dispatched mutation is still verified against
// its own generated binding.
func OperationClaims(r *http.Request) []string {
	if r == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var claims []string
	for _, header := range r.Header.Values(HeaderOperationID) {
		for _, value := range strings.Split(header, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			claims = append(claims, value)
		}
	}
	sort.Strings(claims)
	return claims
}

func VerifyClaim(claims []string, operationID string) error {
	operationID = strings.TrimSpace(operationID)
	if len(claims) == 0 {
		return ErrOperationMissing
	}
	if len(claims) != 1 || strings.TrimSpace(claims[0]) != operationID {
		return fmt.Errorf("%w: claimed %q, dispatched %q", ErrOperationMismatch, claims, operationID)
	}
	return nil
}

func VerifyWorkflowClaims(claims []string, bindings []Binding) error {
	if len(claims) == 0 {
		return ErrOperationMissing
	}
	expected := make([]string, 0, len(bindings))
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		operationID := binding.OperationID()
		if operationID == "" {
			return ErrInvalidBinding
		}
		if _, exists := seen[operationID]; exists {
			continue
		}
		seen[operationID] = struct{}{}
		expected = append(expected, operationID)
	}
	sort.Strings(expected)
	if len(claims) != len(expected) {
		return fmt.Errorf("%w: claimed %q, workflow requires %q", ErrOperationMismatch, claims, expected)
	}
	for index := range expected {
		if claims[index] != expected[index] {
			return fmt.Errorf("%w: claimed %q, workflow requires %q", ErrOperationMismatch, claims, expected)
		}
	}
	return nil
}

// BeginWorkflowInvocation is the explicit exception for a browser event that
// composes multiple generated mutations. It verifies the complete closed set
// and then begins exactly one typed step from that set.
func BeginWorkflowInvocation(
	r *http.Request,
	workflow []Binding,
	binding Binding,
	contract apigencommand.Contract,
	invocation apigencommand.Invocation,
) (context.Context, *apigencommand.Guard, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("%w: request is required", ErrInvalidBinding)
	}
	if binding.OperationID() == "" || binding.OperationID() != contract.OperationID {
		return r.Context(), nil, fmt.Errorf("%w: binding %q uses %q but contract is %q", ErrInvalidBinding, binding.ActionID(), binding.OperationID(), contract.OperationID)
	}
	found := false
	for _, candidate := range workflow {
		if candidate.ActionID() == binding.ActionID() && candidate.OperationID() == binding.OperationID() {
			found = true
			break
		}
	}
	if !found {
		return r.Context(), nil, fmt.Errorf("%w: action %q is not a workflow step", ErrInvalidBinding, binding.ActionID())
	}
	if err := VerifyWorkflowClaims(OperationClaims(r), workflow); err != nil {
		return r.Context(), nil, err
	}
	invocation.OperationID = binding.OperationID()
	invocation.Surface = apigencommand.SurfaceUI
	return apigencommand.BeginInvocation(r.Context(), contract, invocation)
}

// BeginInvocation verifies browser identity and then delegates all generated
// surface, target, idempotency, and concurrency policy to APIGen.
func BeginInvocation(
	r *http.Request,
	binding Binding,
	contract apigencommand.Contract,
	invocation apigencommand.Invocation,
) (context.Context, *apigencommand.Guard, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("%w: request is required", ErrInvalidBinding)
	}
	if binding.OperationID() == "" || binding.OperationID() != contract.OperationID {
		return r.Context(), nil, fmt.Errorf("%w: binding %q uses %q but contract is %q", ErrInvalidBinding, binding.ActionID(), binding.OperationID(), contract.OperationID)
	}
	if err := VerifyClaim(OperationClaims(r), contract.OperationID); err != nil {
		return r.Context(), nil, err
	}
	invocation.OperationID = binding.OperationID()
	invocation.Surface = apigencommand.SurfaceUI
	ctx, guard, err := apigencommand.BeginInvocation(r.Context(), contract, invocation)
	if err != nil {
		return r.Context(), nil, err
	}
	return ctx, guard, nil
}
