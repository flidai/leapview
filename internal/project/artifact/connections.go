package artifact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
)

type ConnectionActivationMode = connectors.ActivationMode

const (
	ManagedActivation       = connectors.ManagedActivation
	AuthoredActivation      = connectors.AuthoredActivation
	TargetBindingActivation = connectors.TargetBindingActivation
)

// ConnectionActivation is the immutable project-level projection of logical
// connections. Target bindings are resolved by deployment; this value carries
// only connector kind and activation mode.
type ConnectionActivation struct {
	LogicalConnectionID string
	ConnectorKind       string
	Mode                ConnectionActivationMode
}

func (p Project) ConnectionActivations() ([]ConnectionActivation, error) {
	connections := p.Connections()
	ids := make([]string, 0, len(connections))
	for id := range connections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ConnectionActivation, 0, len(ids))
	for _, id := range ids {
		connection := connections[id]
		kind := strings.TrimSpace(connection.Kind)
		if strings.TrimSpace(id) != id || id == "" || kind == "" {
			return nil, fmt.Errorf("project connection %q has invalid metadata", id)
		}
		spec, ok := connectors.LookupConnection(kind)
		if !ok {
			return nil, fmt.Errorf("project connection %q has unsupported connector kind %q", id, kind)
		}
		if spec.ActivationMode == "" {
			return nil, fmt.Errorf("project connection %q has no activation mode", id)
		}
		result = append(result, ConnectionActivation{LogicalConnectionID: id, ConnectorKind: kind, Mode: spec.ActivationMode})
	}
	return result, nil
}
