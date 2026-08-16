package appearance

import (
	"errors"
	"fmt"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"strings"
)

var ErrInvalid = errors.New("invalid dashboard appearance")

const (
	DefaultIcon  = "layout-dashboard"
	DefaultColor = "purple"
	ResetValue   = "default"
)

var validColors = map[string]struct{}{
	"gray": {}, "blue": {}, "green": {}, "yellow": {}, "orange": {},
	"red": {}, "purple": {}, "pink": {}, "coral": {},
}

// Patch preserves the distinction between an omitted deployment field and an
// explicit "default" reset. That distinction is what lets dashboard-as-code
// coexist with later cosmetic edits made in the UI.
type Patch struct {
	Icon  *string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Color *string `yaml:"color,omitempty" json:"color,omitempty"`
}

type Value struct {
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

type Key struct {
	ProjectID   projectgraph.ResourceID
	DashboardID projectgraph.ResourceID
}

type Record struct {
	Key
	Value
	Revision int64
}

func Default() Value { return Value{Icon: DefaultIcon, Color: DefaultColor} }

func Resolve(value Value) Value {
	if strings.TrimSpace(value.Icon) == "" {
		value.Icon = DefaultIcon
	}
	if strings.TrimSpace(value.Color) == "" {
		value.Color = DefaultColor
	}
	return value
}

func ValidatePatch(patch Patch) error {
	if patch.Icon != nil {
		icon := strings.TrimSpace(*patch.Icon)
		if icon != ResetValue && !ValidIcon(icon) {
			return fmt.Errorf("%w: unsupported dashboard icon %q", ErrInvalid, icon)
		}
	}
	if patch.Color != nil {
		color := strings.TrimSpace(*patch.Color)
		if color != ResetValue {
			if _, ok := validColors[color]; !ok {
				return fmt.Errorf("%w: unsupported dashboard color %q", ErrInvalid, color)
			}
		}
	}
	return nil
}

func StoredValue(value string) string {
	value = strings.TrimSpace(value)
	if value == ResetValue {
		return ""
	}
	return value
}

func Colors() []string {
	return []string{"gray", "blue", "green", "yellow", "orange", "red", "purple", "pink", "coral"}
}
