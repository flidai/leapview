// Package failure defines transport-neutral command failure kinds and their
// generated public contracts.
package failure

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Contract maps a domain failure kind to stable public behavior.
type Contract struct {
	Kind         string
	StatusCode   int
	Code         string
	PublicDetail string
}

// KindError is a transport-neutral classified error. It may be used as a
// sentinel and remains compatible with errors.Is when wrapped.
type KindError struct {
	kind    string
	message string
	cause   error
}

// New creates a classified sentinel error.
func New(kind, message string) error {
	return &KindError{kind: kind, message: message}
}

// Wrap classifies an existing error while preserving it for errors.Is/As.
func Wrap(kind string, cause error) error {
	if cause == nil {
		return nil
	}
	return &KindError{kind: kind, message: cause.Error(), cause: cause}
}

func (e *KindError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *KindError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// APIGenFailureKind exposes the stable classification without HTTP concepts.
func (e *KindError) APIGenFailureKind() string {
	if e == nil {
		return ""
	}
	return e.kind
}

// KindOf returns the first classified failure kind in an error chain.
func KindOf(err error) (string, bool) {
	var classified interface{ APIGenFailureKind() string }
	if !errors.As(err, &classified) {
		return "", false
	}
	kind := strings.TrimSpace(classified.APIGenFailureKind())
	return kind, kind != ""
}

// Match resolves a classified domain error against generated contracts.
func Match(contracts []Contract, err error) (Contract, bool) {
	kind, ok := KindOf(err)
	if !ok {
		return Contract{}, false
	}
	for _, contract := range contracts {
		if contract.Kind == kind {
			return contract, true
		}
	}
	return Contract{}, false
}

// ValidateContracts rejects malformed or ambiguous generated contracts.
func ValidateContracts(contracts []Contract) error {
	kinds := make(map[string]struct{}, len(contracts))
	codes := make(map[string]struct{}, len(contracts))
	for _, contract := range contracts {
		if !kindPattern.MatchString(contract.Kind) {
			return fmt.Errorf("invalid failure kind %q", contract.Kind)
		}
		if contract.StatusCode < 400 || contract.StatusCode > 599 {
			return fmt.Errorf("failure %q has invalid status %d", contract.Kind, contract.StatusCode)
		}
		if !codePattern.MatchString(contract.Code) {
			return fmt.Errorf("failure %q has invalid code %q", contract.Kind, contract.Code)
		}
		if strings.TrimSpace(contract.PublicDetail) == "" {
			return fmt.Errorf("failure %q requires public detail", contract.Kind)
		}
		if _, exists := kinds[contract.Kind]; exists {
			return fmt.Errorf("duplicate failure kind %q", contract.Kind)
		}
		if _, exists := codes[contract.Code]; exists {
			return fmt.Errorf("duplicate failure code %q", contract.Code)
		}
		kinds[contract.Kind] = struct{}{}
		codes[contract.Code] = struct{}{}
	}
	return nil
}
