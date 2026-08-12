package module

import "github.com/flidai/leapview/internal/workspace"

// SettingsAdministration is the workspace-owned read surface used by product
// settings. Persistence remains assembled inside the workspace capability.
type SettingsAdministration interface {
	workspace.ReadModel
	workspace.AdministrationReadModel
}

func (m *Module) SettingsAdministration() SettingsAdministration {
	if m == nil {
		return nil
	}
	settings, _ := m.readModel.(SettingsAdministration)
	return settings
}
