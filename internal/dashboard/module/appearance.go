package module

import (
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
)

type Appearance = dashboardappearance.Value
type AppearanceRecord = dashboardappearance.Record

func DefaultAppearance() Appearance {
	return dashboardappearance.Default()
}

func ResolveAppearance(value Appearance) Appearance {
	return dashboardappearance.Resolve(value)
}
