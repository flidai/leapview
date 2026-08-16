package connectionbinding

import (
	"fmt"
	"regexp"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// BindingID identifies a durable target binding record. It is an operational
// identity, not a project-graph resource.
type BindingID string
type TargetID string

// LogicalConnectionID is retained as a source-compatibility alias for older
// adapters. Runtime binding identity is now the project-graph ConnectionID;
// new code should use ParseConnectionID directly.
type LogicalConnectionID = projectgraph.ResourceID

// ParseLogicalConnectionID is the deprecated spelling of ParseConnectionID.
func ParseLogicalConnectionID(value string) (LogicalConnectionID, error) {
	return ParseConnectionID(value)
}

func (id BindingID) String() string { return string(id) }
func (id TargetID) String() string  { return string(id) }

var bindingIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$`)

func ParseBindingID(value string) (BindingID, error) {
	if value != strings.TrimSpace(value) || !bindingIDPattern.MatchString(value) {
		return "", fmt.Errorf("%w: binding id must be canonical", ErrInvalidBinding)
	}
	return BindingID(value), nil
}

func ParseTargetID(value string) (TargetID, error) {
	if value != strings.TrimSpace(value) || !bindingIDPattern.MatchString(value) {
		return "", fmt.Errorf("%w: target id must be canonical", ErrInvalidBinding)
	}
	return TargetID(value), nil
}
