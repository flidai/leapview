package artifact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/sourcedataidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	Access              semanticmodel.ConnectionAccess
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
		if connection.Access != "" && connection.Access != semanticmodel.ConnectionAccessPublic {
			return nil, fmt.Errorf("project connection %q has unsupported access policy %q", id, connection.Access)
		}
		if connection.Access == semanticmodel.ConnectionAccessPublic && !spec.AllowPublicAccess {
			return nil, fmt.Errorf("project connection %q connector %q does not support public access", id, kind)
		}
		result = append(result, ConnectionActivation{LogicalConnectionID: id, ConnectorKind: kind, Mode: spec.ActivationMode, Access: connection.Access})
	}
	return result, nil
}

// SourceDataIdentityEvidence adapts authoritative managed-content revisions
// into source-scoped equivalence evidence. Connectors without an admitted
// identity capability, missing revisions, malformed revisions, and malformed
// source mappings are deliberately omitted so callers fail closed per source.
// The admitted runtime binding kind must exactly match the artifact connector;
// a revision is never trusted across a binding mismatch.
func (p Project) SourceDataIdentityEvidence(revisions, bindingKinds map[string]string) (map[projectgraph.ResourceID]sourcedataidentity.Evidence, error) {
	manifest := p.Manifest()
	aliasCapacity, err := sourceDataIdentityAliasCapacity(len(manifest.Connections))
	if err != nil {
		return nil, err
	}
	connectionIDs := make(map[string]string, aliasCapacity)
	for _, resource := range p.Graph().Resources() {
		if resource.Kind != projectgraph.KindConnection {
			continue
		}
		connectionIDs[resource.ID.String()] = resource.ID.String()
		connectionIDs[resource.Name] = resource.ID.String()
	}
	evidence := make(map[projectgraph.ResourceID]sourcedataidentity.Evidence)
	for sourceID, source := range manifest.Sources {
		parsedSourceID, err := projectgraph.NewResourceID(sourceID)
		if err != nil {
			continue
		}
		connectionReference := source.Connection
		if connectionReference == "" || connectionReference != strings.TrimSpace(connectionReference) {
			continue
		}
		connectionID := connectionIDs[connectionReference]
		connection, ok := manifest.Connections[connectionID]
		if !ok {
			continue
		}
		connectorKind := connection.Kind
		if connectorKind == "" || connectorKind != strings.TrimSpace(connectorKind) {
			continue
		}
		spec, ok := connectors.LookupConnection(connectorKind)
		if !ok || spec.SourceDataIdentityCapability != connectors.SourceDataIdentityContentRevision {
			continue
		}
		bindingKind := bindingKinds[connectionID]
		if bindingKind == "" || bindingKind != strings.TrimSpace(bindingKind) || bindingKind != spec.Kind {
			continue
		}
		item, err := sourcedataidentity.NewEvidence(sourcedataidentity.EvidenceInput{
			SourceID: parsedSourceID, RevisionDigest: revisions[connectionID],
		})
		if err != nil {
			continue
		}
		evidence[parsedSourceID] = item
	}
	return evidence, nil
}

func sourceDataIdentityAliasCapacity(connectionCount int) (int, error) {
	maximumInt := int(^uint(0) >> 1)
	if connectionCount < 0 || connectionCount > maximumInt/2 {
		return 0, fmt.Errorf("source data identity connection alias capacity cannot be represented")
	}
	return connectionCount * 2, nil
}
