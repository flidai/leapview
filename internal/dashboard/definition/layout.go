package definition

import "fmt"

// NarrowViewPolicy identifies the renderer-owned behavior used when a page is
// narrower than its authored grid. Dashboard authoring deliberately cannot
// select breakpoints or a viewport width; the policy is fixed by the serving
// contract and therefore deterministic across renderers.
type NarrowViewPolicy string

const (
	// NarrowViewPolicyStack preserves component order and stacks every
	// component into one responsive column.
	NarrowViewPolicyStack NarrowViewPolicy = "stack"
)

// LayoutDefaults is the immutable dashboard grid contract inherited by every
// page unless a page supplies a documented override.
type LayoutDefaults struct {
	Columns   int `json:"columns"`
	RowHeight int `json:"rowHeight"`
	Gap       int `json:"gap"`
	Padding   int `json:"padding"`
}

// Layout is the immutable dashboard-level layout artifact. Resolved page grid,
// derived height, and component placement remain on the existing dashboard
// Page/PageVisual runtime structures; this value contains only inherited
// dashboard defaults and the fixed renderer policy.
type Layout struct {
	Defaults   LayoutDefaults   `json:"defaults"`
	NarrowView NarrowViewPolicy `json:"narrowView"`
}

func (layout Layout) Validate() error {
	if err := validateLayoutDefaults(layout.Defaults); err != nil {
		return err
	}
	if layout.NarrowView != NarrowViewPolicyStack {
		return fmt.Errorf("unsupported narrow-view policy %q", layout.NarrowView)
	}
	return nil
}

func validateLayoutDefaults(value LayoutDefaults) error {
	if value.Columns <= 0 {
		return fmt.Errorf("layout columns must be greater than zero")
	}
	if value.RowHeight <= 0 {
		return fmt.Errorf("layout rowHeight must be greater than zero")
	}
	if value.Gap < 0 {
		return fmt.Errorf("layout gap must be non-negative")
	}
	if value.Padding < 0 {
		return fmt.Errorf("layout padding must be non-negative")
	}
	return nil
}
