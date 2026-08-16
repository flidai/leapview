package runtimefactory

import (
	"fmt"
	"sort"

	manageddataruntimebinding "github.com/flidai/leapview/internal/manageddata/runtimebinding"
	"github.com/flidai/leapview/internal/project/manifest"
)

type projectBindingTarget struct {
	definition *manifest.Project
}

func bindManagedDataRoots(definition *manifest.Project, roots map[string]string) error {
	if definition == nil {
		return fmt.Errorf("project definition is required")
	}
	return manageddataruntimebinding.BindRoots(projectBindingTarget{definition: definition}, roots)
}

func (t projectBindingTarget) ManagedConnections() []manageddataruntimebinding.Connection {
	var connections []manageddataruntimebinding.Connection
	for modelID, model := range t.definition.SemanticModels {
		if model == nil {
			continue
		}
		for name, connection := range model.Connections {
			if connection.Kind == "managed" {
				connections = append(connections, manageddataruntimebinding.Connection{ModelID: modelID, Name: name})
			}
		}
	}
	sort.Slice(connections, func(i, j int) bool {
		if connections[i].ModelID == connections[j].ModelID {
			return connections[i].Name < connections[j].Name
		}
		return connections[i].ModelID < connections[j].ModelID
	})
	return connections
}

func (t projectBindingTarget) BindManagedRoot(ref manageddataruntimebinding.Connection, root string) error {
	model := t.definition.SemanticModels[ref.ModelID]
	if model == nil {
		return fmt.Errorf("semantic model %q is unavailable while binding managed data", ref.ModelID)
	}
	connection, ok := model.Connections[ref.Name]
	if !ok || connection.Kind != "managed" {
		return fmt.Errorf("semantic model %q managed connection %q is unavailable", ref.ModelID, ref.Name)
	}
	connection.Root = root
	connection.Scope = ""
	model.Connections[ref.Name] = connection
	return nil
}
