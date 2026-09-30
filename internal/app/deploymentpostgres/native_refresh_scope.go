package deploymentpostgres

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/deployment"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	"github.com/flidai/leapview/internal/release"
)

// validateNativeRefreshScope fences the work the current native materializer
// actually performs. It prepares every source and materializes every authored
// Model; a partial pipeline cannot authorize that full-project work. This check
// runs after read-only artifact inspection, before binding/pool/root resolution.
func validateNativeRefreshScope(artifacts release.CandidateArtifactSet, pipeline *projectpipelineplan.Plan) error {
	if pipeline == nil {
		return nil
	}
	deny := func() error {
		return fmt.Errorf("%w: native candidate work differs from captured refresh scope", deployment.ErrDeliveryConflict)
	}
	plan := pipeline.Canonical()
	identity := artifacts.Generation.Identity
	if plan.Validate() != nil || identity.Validate() != nil || plan.ProjectID != identity.ProjectID.String() || plan.Environment != identity.Environment {
		return deny()
	}
	definition := artifacts.Compiler.Artifact.RefreshDefinition()
	// Rebuild from the retained candidate to bind the connection name-to-ID
	// mapping to the queued digest, not merely to the candidate's own claims.
	rebuilt, err := refreshplan.ForPipeline(definition, identity.ProjectID, projectgraph.ResourceID(plan.PipelineID))
	if err != nil || rebuilt.SemanticModelID.String() != plan.SemanticModelID ||
		rebuilt.BindingDigest != plan.BindingDigest || rebuilt.SelectionDigest != plan.SelectionDigest ||
		!slices.Equal(rebuilt.MaterializationScope, plan.MaterializationScope) || !slices.Equal(rebuilt.SourceInputs, plan.SourceInputs) {
		return deny()
	}
	if len(definition.ModelTables) != len(plan.MaterializationScope) {
		return deny()
	}
	for _, name := range plan.MaterializationScope {
		if _, ok := definition.ModelTables[name]; !ok {
			return deny()
		}
	}
	selected := definition.Models[plan.SemanticModelID]
	if selected == nil {
		return deny()
	}
	capturedSources := make(map[string]struct{}, len(plan.SourceInputs))
	for _, source := range plan.SourceInputs {
		capturedSources[source] = struct{}{}
	}
	actualSources := make(map[string]struct{}, len(capturedSources))
	for _, model := range definition.Models {
		if model == nil {
			return deny()
		}
		for name, source := range model.Sources {
			if _, ok := capturedSources[name]; !ok {
				return deny()
			}
			selectedSource, ok := selected.Sources[name]
			if !ok || source.Connection != selectedSource.Connection {
				return deny()
			}
			actualSources[name] = struct{}{}
		}
	}
	if len(actualSources) != len(capturedSources) {
		return deny()
	}
	required, err := refreshplan.RequiredConnectionIDs(definition, rebuilt)
	if err != nil {
		return deny()
	}
	allowed := make(map[projectgraph.ResourceID]struct{}, len(required))
	for _, connectionID := range required {
		allowed[connectionID] = struct{}{}
	}
	manifest := artifacts.Compiler.Artifact.Manifest()
	seen := make(map[projectgraph.ResourceID]struct{}, len(required))
	checkConnection := func(id projectgraph.ResourceID, kind string, access semanticmodel.ConnectionAccess, mode connectors.ActivationMode) bool {
		if !id.Valid() || id.String() != strings.TrimSpace(id.String()) {
			return false
		}
		if _, ok := allowed[id]; !ok {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		connection, ok := manifest.Connections[id.String()]
		if !ok || connection.Kind != kind || connection.Access != access {
			return false
		}
		spec, ok := connectors.LookupConnection(kind)
		if !ok || spec.ActivationMode != mode {
			return false
		}
		seen[id] = struct{}{}
		return true
	}
	for _, connection := range artifacts.Generation.Connections {
		if !checkConnection(connection.ConnectionID, connection.ConnectorKind, connection.Access, connectors.TargetBindingActivation) {
			return deny()
		}
	}
	for _, connection := range artifacts.Generation.AuthoredConnections {
		if !checkConnection(connection.ConnectionID, connection.ConnectorKind, connection.Access, connectors.AuthoredActivation) {
			return deny()
		}
	}
	for _, pin := range artifacts.Generation.ManagedDataPins {
		connection, ok := manifest.Connections[pin.ConnectionID]
		if !ok || !checkConnection(projectgraph.ResourceID(pin.ConnectionID), connection.Kind, connection.Access, connectors.ManagedActivation) {
			return deny()
		}
	}
	if len(seen) != len(allowed) {
		return deny()
	}
	return nil
}
