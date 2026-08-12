// Package ui defines generated, transport-neutral browser action bindings.
package ui

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var actionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*(\.[a-z][a-z0-9]*(-[a-z0-9]+)*)+$`)

var ErrInvalidAction = errors.New("invalid generated UI action")

// Action gives one stable browser action exactly one generated command.
// Its fields are private so callers cannot construct a drifting binding.
type Action struct {
	actionID    string
	operationID string
}

// NewAction validates and constructs a generated UI action binding.
func NewAction(actionID, operationID string) (Action, error) {
	actionID = strings.TrimSpace(actionID)
	operationID = strings.TrimSpace(operationID)
	if !actionIDPattern.MatchString(actionID) || operationID == "" {
		return Action{}, fmt.Errorf("%w: stable action and operation IDs are required", ErrInvalidAction)
	}
	return Action{actionID: actionID, operationID: operationID}, nil
}

// MustAction constructs an Action or panics when generated metadata is invalid.
func MustAction(actionID, operationID string) Action {
	action, err := NewAction(actionID, operationID)
	if err != nil {
		panic(err)
	}
	return action
}

func (a Action) ActionID() string    { return a.actionID }
func (a Action) OperationID() string { return a.operationID }
func (a Action) Valid() bool         { return a.actionID != "" && a.operationID != "" }
