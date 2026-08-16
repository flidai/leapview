package connectionbinding

import (
	"fmt"
	"regexp"
	"strings"
)

// BindingID identifies a durable target binding record. It is an operational
// identity, not a project-graph resource.
type BindingID string

var bindingIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$`)

func ParseBindingID(value string) (BindingID, error) {
	if value != strings.TrimSpace(value) || !bindingIDPattern.MatchString(value) {
		return "", fmt.Errorf("%w: binding id must be canonical", ErrInvalidBinding)
	}
	return BindingID(value), nil
}
