package module

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/extension"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

type candidateArtifactInspector struct {
	pins                 ManagedDataPins
	extensionPreparation extension.Preparation
}

type candidateGenerationBase struct {
	graph           projectgraph.ProjectGraph
	artifact        projectartifact.Project
	pins            map[string]string
	bindings        map[string]string
	snapshotID      int64
	dataRevision    string
	relationContext map[string]string
	gateEvidence    *release.GateEvidence
	active          bool
}

func planCandidateProject(projectPath string, base candidateGenerationBase) (projectcompiler.ProjectPlan, error) {
	if base.active {
		return projectcompiler.PlanProjectAgainstArtifact(projectPath, base.artifact)
	}
	return projectcompiler.PlanProjectAgainstGraph(projectPath, projectgraph.ProjectGraph{})
}

// InspectCandidateArtifacts is the plan-phase, read-only half of candidate
// preparation. It compiles the retained source, computes graph impact against
// the exact active base, resolves managed-data pins, and derives authorization
// evidence. It intentionally does not create serving rows, upload artifacts,
// acquire connector credentials, or touch physical catalogs.
func (service *candidateArtifactInspector) inspectCandidateProjectPlan(ctx context.Context, request release.CandidateArtifactRequest, compiledProject projectartifact.Project, plan projectcompiler.ProjectPlan, base candidateGenerationBase) (release.CandidateArtifactSet, error) {
	activations, err := compiledProject.ConnectionActivations()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	requirements, managed, authored, err := candidateConnectionRequirements(activations)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	managedPins := candidateManagedDataPinMap(managed, base.pins)
	missing := missingCandidateManagedConnections(managed, managedPins)
	if len(missing) > 0 {
		if service.pins == nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(errors.New("managed-data candidate pin resolution is unavailable"))
		}
		missingIDs := make([]projectgraph.ResourceID, len(missing))
		for i, connection := range missing {
			id, parseErr := projectgraph.NewResourceID(connection)
			if parseErr != nil {
				return release.CandidateArtifactSet{}, candidateArtifactInvalid(parseErr)
			}
			missingIDs[i] = id
		}
		resolved, resolveErr := service.pins.ResolveCandidatePins(ctx, request.Scope.ProjectID, missingIDs, request.Scope.Environment)
		if resolveErr != nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(resolveErr)
		}
		missingSet := make(map[string]struct{}, len(missing))
		for _, value := range missing {
			missingSet[value] = struct{}{}
		}
		for connection, revision := range resolved {
			if _, wanted := missingSet[connection.String()]; wanted {
				managedPins[connection.String()] = revision
			}
		}
		for _, connection := range missing {
			if _, ok := managedPins[connection]; !ok {
				return release.CandidateArtifactSet{}, candidateArtifactUnavailable(errors.New("managed-data candidate pin resolution returned incomplete result"))
			}
		}
	}
	policyIdentity, err := candidatePolicyIdentity(request.Scope.ProjectID, request.Scope.Environment)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	authorizationSnapshot, err := projectmanifest.CompileAuthorizationSnapshot(policyIdentity, compiledProject.Graph(), compiledProject.Manifest().Access)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	authorizationFingerprint, err := authorizationSnapshot.Digest()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	dataMode := release.GenerationDataRefreshSources
	dataRevision, err := release.CandidateSourcesDataRevision(request.ArtifactDigest, candidateManagedDataPins(managedPins))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if base.active && base.dataRevision != "" && !plan.Summary.MaterializationImpact && base.graph.Validate() == nil {
		dataMode = release.GenerationDataReuseBase
		dataRevision = base.dataRevision
		if dataRevision == "" && base.snapshotID > 0 {
			dataRevision = fmt.Sprintf("snapshot:%d", base.snapshotID)
		}
		authored = nil
	}
	if dataMode == release.GenerationDataRefreshSources && len(requirements) == 0 && len(managedPins) == 0 && len(authored) == 0 {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("project requires data preparation but has no refresh-capable connections"))
	}
	extensionRequirements, requirementErr := requiredExtensionNames(activations, compiledProject.Manifest())
	if requirementErr != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(requirementErr)
	}
	extensions, err := service.collectExtensionEvidence(ctx, extensionRequirements)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	inspectIdentity, err := projectgraph.NewServingIdentity(request.Scope.ProjectID, request.Scope.Environment, "inspect-"+shortCandidateDigest(request.CandidateID))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	relationContext, err := candidateRelationContexts(managedPins, compiledProject, candidateActivationBindings(activations))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	relationExecution, err := compiledProject.RelationExecutionDigestsByContext(relationContext)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	baseRelationExecution, err := base.artifact.RelationExecutionDigestsByContext(base.relationContext)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return release.CandidateArtifactSet{Artifact: release.ProjectArtifactProvenance{SourceDigest: request.ArtifactDigest, ProjectDigest: compiledProject.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: compiledProject.Version()}, Extensions: extensions, AuthorizationFingerprint: authorizationFingerprint, Generation: release.CandidateGenerationArtifact{Identity: inspectIdentity, DataRevision: dataRevision, DataMode: dataMode, Deterministic: plan.Deterministic, ManagedDataPins: candidateManagedDataPins(managedPins), Connections: requirements, AuthoredConnections: authored, Restrictions: candidateRestrictions(authorizationSnapshot), BaseGateEvidence: base.gateEvidence}, Compiler: release.CandidateCompilerEvidence{Graph: compiledProject.Graph(), Manifest: compiledProject.Manifest(), Plan: plan, Artifact: compiledProject, RelationExecution: relationExecution, BaseRelationExecution: baseRelationExecution}}, nil
}

func shortCandidateDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

const candidatePolicyGenerationID = "candidate-policy"

// candidatePolicyIdentity is a stable, non-runtime identity used only when
// hashing candidate authorization policy evidence. The policy fingerprint
// must remain unchanged as a candidate moves between concrete serving
// generations; runtime snapshots retain their actual generation identity.
func candidatePolicyIdentity(projectID projectgraph.ResourceID, environment string) (projectgraph.ServingIdentity, error) {
	return projectgraph.NewServingIdentity(projectID, environment, candidatePolicyGenerationID)
}

func candidateRelationContexts(pins map[string]string, artifact projectartifact.Project, bindingKinds ...map[string]string) (map[string]string, error) {
	var kinds map[string]string
	if len(bindingKinds) > 0 {
		kinds = bindingKinds[0]
	}
	return artifact.RelationExecutionContexts(pins, kinds)
}

func candidateActivationBindings(activations []projectartifact.ConnectionActivation) map[string]string {
	bindings := make(map[string]string, len(activations))
	for _, activation := range activations {
		if connectionID := strings.TrimSpace(activation.LogicalConnectionID); connectionID != "" {
			if kind := strings.TrimSpace(activation.ConnectorKind); kind != "" {
				bindings[connectionID] = kind
			}
		}
	}
	return bindings
}

func candidateRestrictions(snapshot accesssnapshot.AuthorizationSnapshot) []release.CandidateRestriction {
	policies := snapshot.DataPolicies()
	result := make([]release.CandidateRestriction, 0, len(policies))
	for _, policy := range policies {
		var subject *access.SubjectRef
		if policy.Subject != nil {
			copy := *policy.Subject
			subject = &copy
		}
		result = append(result, release.CandidateRestriction{
			ID: policy.ID, ObjectID: policy.Resource.ID(), ObjectKind: policy.Resource.Kind(), Subject: subject,
			PolicyType: policy.PolicyType, ExpressionJSON: policy.ExpressionJSON,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (service *candidateArtifactInspector) collectExtensionEvidence(ctx context.Context, requirements []string) ([]extension.Evidence, error) {
	if service == nil || service.extensionPreparation == nil {
		return nil, errors.New("extension preparation is unavailable")
	}
	values, err := service.extensionPreparation.PrepareExtensions(ctx, requirements)
	if err != nil {
		return nil, err
	}
	values = append([]extension.Evidence(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	for i := range values {
		if values[i].Name == "" || values[i].Identity == "" || values[i].DuckDBVersion == "" || values[i].ExtensionVersion == "" || values[i].GOOS == "" || values[i].GOARCH == "" || values[i].Platform == "" || values[i].SupportProfile == "" || values[i].Digest == "" || values[i].Origin == "" || values[i].Provenance == "" || values[i].Signature == "" {
			return nil, errors.New("extension evidence is incomplete")
		}
		if i > 0 && values[i-1].Name == values[i].Name {
			return nil, errors.New("duplicate extension evidence")
		}
	}
	if len(values) != len(requirements) {
		return nil, errors.New("extension preparation returned incomplete evidence")
	}
	required := make(map[string]struct{}, len(requirements))
	for _, name := range requirements {
		required[name] = struct{}{}
	}
	for _, value := range values {
		if _, ok := required[value.Name]; !ok {
			return nil, errors.New("extension preparation returned unrequested evidence")
		}
	}
	return values, nil
}

func requiredExtensionNames(activations []projectartifact.ConnectionActivation, manifest projectmanifest.Project) ([]string, error) {
	set := map[string]struct{}{"ducklake": {}}
	for _, activation := range activations {
		profile, ok := projectcontracts.LookupConnector(activation.ConnectorKind)
		if !ok {
			return nil, fmt.Errorf("connector %q is absent from generated registry", activation.ConnectorKind)
		}
		for _, name := range profile.ApprovedExtensions {
			set[name] = struct{}{}
		}
	}
	// Format extensions (excel/delta/etc.) are also exact generated runtime
	// requirements. They are derived from the compiled manifest, never from
	// authored SQL at candidate activation time.
	for _, source := range manifest.Sources {
		if format, ok := connectors.LookupFormat(source.Format); ok && format.RequiredExtension != "" {
			set[format.RequiredExtension] = struct{}{}
		}
	}
	// DuckDB's Iceberg extension loads the official Avro dependency when it is
	// initialized. Admit both artifacts in sorted order so the dependency is
	// present before the offline Iceberg LOAD runs.
	if _, ok := set["iceberg"]; ok {
		set["avro"] = struct{}{}
	}
	values := make([]string, 0, len(set))
	for name := range set {
		values = append(values, name)
	}
	sort.Strings(values)
	return values, nil
}

func candidateConnectionRequirements(activations []projectartifact.ConnectionActivation) ([]release.CandidateConnectionRequirement, []string, []release.CandidateAuthoredConnection, error) {
	requirements := make([]release.CandidateConnectionRequirement, 0, len(activations))
	managed := make([]string, 0, len(activations))
	authored := make([]release.CandidateAuthoredConnection, 0, len(activations))
	for _, activation := range activations {
		connectionID, err := projectgraph.NewResourceID(activation.LogicalConnectionID)
		if err != nil {
			return nil, nil, nil, err
		}
		switch activation.Mode {
		case projectartifact.ManagedActivation:
			managed = append(managed, activation.LogicalConnectionID)
		case projectartifact.AuthoredActivation:
			authored = append(authored, release.CandidateAuthoredConnection{ConnectionID: connectionID, ConnectorKind: activation.ConnectorKind, Access: activation.Access})
		case projectartifact.TargetBindingActivation:
			requirements = append(requirements, release.CandidateConnectionRequirement{ConnectionID: connectionID, ConnectorKind: activation.ConnectorKind, Access: activation.Access})
		}
	}
	return requirements, managed, authored, nil
}

func candidateManagedDataPins(values map[string]string) []release.ManagedDataPin {
	connections := make([]string, 0, len(values))
	for connection := range values {
		connections = append(connections, connection)
	}
	sort.Strings(connections)
	result := make([]release.ManagedDataPin, 0, len(connections))
	for _, connection := range connections {
		result = append(result, release.ManagedDataPin{ConnectionID: connection, RevisionID: values[connection]})
	}
	return result
}

func candidateManagedDataPinMap(managed []string, base map[string]string) map[string]string {
	pins := make(map[string]string, len(managed))
	for _, connection := range managed {
		if revision, ok := base[connection]; ok {
			pins[connection] = revision
		}
	}
	return pins
}

func missingCandidateManagedConnections(connections []string, pins map[string]string) []string {
	result := make([]string, 0)
	for _, connection := range connections {
		if _, ok := pins[connection]; !ok {
			result = append(result, connection)
		}
	}
	return result
}

func candidateArtifactInvalid(err error) error {
	return fmt.Errorf("%w: %v", release.ErrCandidateArtifactInvalid, err)
}
func candidateArtifactUnavailable(err error) error {
	return fmt.Errorf("%w: %v", release.ErrCandidateArtifactUnavailable, err)
}
