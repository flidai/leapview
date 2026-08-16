package analyticsruntime

import (
	"fmt"
	"sort"

	manageddataruntimebinding "github.com/flidai/leapview/internal/manageddata/runtimebinding"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
)

type bindingTarget struct {
	definition *refreshartifact.Definition
}

func bindManagedDataRoots(definition *refreshartifact.Definition, roots map[string]string) error {
	if definition == nil {
		return fmt.Errorf("project definition is required")
	}
	return manageddataruntimebinding.BindRoots(bindingTarget{definition: definition}, roots)
}

func (t bindingTarget) ManagedConnections() []manageddataruntimebinding.Connection {
	var connections []manageddataruntimebinding.Connection
	for modelID, model := range t.definition.Models {
		if model == nil {
			continue
		}
		for name, connection := range model.Connections {
			if connection.Kind == "managed" {
				connections = append(connections, manageddataruntimebinding.Connection{ModelID: modelID, Name: name, ID: t.definition.ConnectionIDs[name]})
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

func (t bindingTarget) BindManagedRoot(ref manageddataruntimebinding.Connection, root string) error {
	model := t.definition.Models[ref.ModelID]
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
