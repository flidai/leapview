package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatevalidate "github.com/flidai/leapview/internal/servingstate/validate"
)

type candidateArtifactService struct {
	states               ServingStateRepository
	artifacts            release.ArtifactStore
	validator            servingstatevalidate.Service
	environment          servingstate.Environment
	pins                 ManagedDataPins
	provenance           release.ServingStateProvenanceRepository
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
func (service *candidateArtifactService) InspectCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	if service == nil || service.states == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if request.CandidateID != strings.TrimSpace(request.CandidateID) || request.OwnerID != strings.TrimSpace(request.OwnerID) || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) || request.Source.ArtifactDigest != strings.TrimSpace(request.Source.ArtifactDigest) || request.Source.ProjectPath != strings.TrimSpace(request.Source.ProjectPath) || request.Source.ProjectDigest != strings.TrimSpace(request.Source.ProjectDigest) || request.Source.ProjectArtifactPath != strings.TrimSpace(request.Source.ProjectArtifactPath) {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	if request.Scope.Validate() != nil || request.OwnerID == "" || request.Source.ProjectID.Validate() != nil || request.Source.ProjectID != request.Scope.ProjectID || request.Source.ArtifactDigest != request.ArtifactDigest || platformdigest.ValidateSHA256Identity(request.ArtifactDigest) != nil || platformdigest.ValidateSHA256Identity(request.Source.ProjectDigest) != nil || request.Scope.Environment != string(service.environment) {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	projectBytes, err := securefs.ReadCanonicalRegularFile(request.Source.ProjectArtifactPath)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	compiledProject, err := projectartifact.Decode(projectBytes)
	if err != nil || compiledProject.ProjectID() != request.Scope.ProjectID || compiledProject.Digest() != request.Source.ProjectDigest {
		if err == nil {
			err = fmt.Errorf("retained project artifact does not match synchronized project")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	baseIdentity, err := request.Scope.BaseIdentity()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	base, err := service.generationBase(ctx, baseIdentity)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	return service.inspectCandidateProject(ctx, request, compiledProject, request.Source.ProjectPath, base)
}

// inspectCandidateProject contains the shared compiler/evidence logic used by
// both the legacy filesystem-backed path and the native object-backed path.
// Native callers use inspectCandidateProjectPlan with logical source bytes.
func (service *candidateArtifactService) inspectCandidateProject(ctx context.Context, request release.CandidateArtifactRequest, compiledProject projectartifact.Project, projectPath string, base candidateGenerationBase) (release.CandidateArtifactSet, error) {
	plan, err := planCandidateProject(projectPath, base)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return service.inspectCandidateProjectPlan(ctx, request, compiledProject, plan, base)
}

func (service *candidateArtifactService) inspectCandidateProjectPlan(ctx context.Context, request release.CandidateArtifactRequest, compiledProject projectartifact.Project, plan projectcompiler.ProjectPlan, base candidateGenerationBase) (release.CandidateArtifactSet, error) {
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
	authorizationSnapshot, err := projectmanifest.CompileAuthorizationSnapshot(projectgraph.ServingIdentity{ProjectID: request.Scope.ProjectID, Environment: request.Scope.Environment, GenerationID: "inspect"}, compiledProject.Graph(), compiledProject.Manifest().Access)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	authorizationFingerprint, err := authorizationSnapshot.Digest()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	dataMode := release.GenerationDataRefreshSources
	dataRevision, err := candidateSourcesDataRevision(request.ArtifactDigest, managedPins)
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

func (service *candidateArtifactService) MaterializeCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	return service.prepare(ctx, request, &inspected)
}

// HydrateCandidateArtifacts reattaches a deterministic serving artifact that
// was prepared and durably bound to an earlier attempt. It performs no writes
// and preserves the inspected compiler evidence used by planning.
func (service *candidateArtifactService) HydrateCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet, identity release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error) {
	if service == nil || service.states == nil || identity.ServingStateID == "" || identity.ServingArtifactID == "" || identity.ServingArtifactDigest == "" {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	state, err := service.states.ByID(ctx, servingstate.ID(identity.ServingStateID))
	if err != nil || state.ProjectID != request.Scope.ProjectID || state.Environment != servingstate.Environment(request.Scope.Environment) || state.Digest == "" || state.Digest != identity.ServingArtifactDigest {
		if err == nil {
			err = errors.New("durable serving state identity mismatch")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	artifact, err := service.states.ArtifactByServingState(ctx, state.ID)
	if err != nil || artifact.ID != identity.ServingArtifactID || artifact.Digest != identity.ServingArtifactDigest || artifact.ServingStateID != state.ID {
		if err == nil {
			err = errors.New("durable serving artifact identity mismatch")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if artifact.Path == "" {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("durable serving artifact path is empty"))
	}
	validation, err := projectbundle.ValidateArtifact(artifact.Path)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	if validation.ProjectID != request.Scope.ProjectID.String() || validation.Digest != artifact.Digest || validation.ProjectDigest != state.ProjectDigest || validation.ManifestJSON != artifact.ManifestJSON || validation.ManifestJSON != state.ManifestJSON {
		if validation.RootDir != "" {
			_ = os.RemoveAll(validation.RootDir)
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("durable serving artifact content identity mismatch"))
	}
	if validation.RootDir != "" {
		defer os.RemoveAll(validation.RootDir)
	}
	result := inspected
	result.Artifact.ContentDigest = artifact.Digest
	result.Generation.Identity, err = projectgraph.NewServingIdentity(request.Scope.ProjectID, request.Scope.Environment, string(state.ID))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result.Generation.ServingArtifactID = artifact.ID
	result.Generation.ArtifactDigest = artifact.Digest
	if err := result.Generation.Identity.Validate(); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return result, nil
}

// Prepare creates exactly one project-generation serving artifact for one
// candidate target.
func (service *candidateArtifactService) Prepare(ctx context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	return service.prepare(ctx, request, nil)
}

func (service *candidateArtifactService) prepare(ctx context.Context, request release.CandidateArtifactRequest, expected *release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	if request.CandidateID != strings.TrimSpace(request.CandidateID) || request.OwnerID != strings.TrimSpace(request.OwnerID) || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) || request.Source.ArtifactDigest != strings.TrimSpace(request.Source.ArtifactDigest) || request.Source.ProjectPath != strings.TrimSpace(request.Source.ProjectPath) || request.Source.ProjectDigest != strings.TrimSpace(request.Source.ProjectDigest) || request.Source.ProjectArtifactPath != strings.TrimSpace(request.Source.ProjectArtifactPath) {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	if service == nil || service.states == nil || service.artifacts == nil || request.CandidateID == "" || request.Scope.Validate() != nil || request.OwnerID == "" || request.Source.ProjectArtifactPath == "" || request.Source.ProjectID.Validate() != nil || request.Source.ProjectID != request.Scope.ProjectID || request.Source.ArtifactDigest != request.ArtifactDigest || platformdigest.ValidateSHA256Identity(request.ArtifactDigest) != nil || platformdigest.ValidateSHA256Identity(request.Source.ProjectDigest) != nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	if request.Scope.Environment != string(service.environment) {
		return release.CandidateArtifactSet{}, fmt.Errorf("%w: candidate environment does not match target", release.ErrCandidateArtifactInvalid)
	}
	projectBytes, err := securefs.ReadCanonicalRegularFile(request.Source.ProjectArtifactPath)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	compiledProject, err := projectartifact.Decode(projectBytes)
	if err != nil || compiledProject.ProjectID() != request.Scope.ProjectID || compiledProject.Digest() != request.Source.ProjectDigest {
		if err == nil {
			err = fmt.Errorf("retained project artifact does not match synchronized project")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	projectID := request.Scope.ProjectID
	environment := servingstate.Environment(request.Scope.Environment)
	baseIdentity, identityErr := request.Scope.BaseIdentity()
	if identityErr != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(identityErr)
	}
	base, err := service.generationBase(ctx, baseIdentity)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	plan, err := planCandidateProject(request.Source.ProjectPath, base)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
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
		for index, connection := range missing {
			connectionID, parseErr := projectgraph.NewResourceID(connection)
			if parseErr != nil {
				return release.CandidateArtifactSet{}, candidateArtifactInvalid(parseErr)
			}
			missingIDs[index] = connectionID
		}
		missingSet := make(map[string]struct{}, len(missing))
		for _, connection := range missing {
			missingSet[connection] = struct{}{}
		}
		resolved, resolveErr := service.pins.ResolveCandidatePins(ctx, projectID, missingIDs, string(environment))
		if resolveErr != nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(resolveErr)
		}
		for connection, revision := range resolved {
			if _, requested := missingSet[connection.String()]; !requested {
				// A resolver must return only requested IDs. Ignore any
				// unexpected result so stale base pins cannot leak into the
				// candidate generation.
				continue
			}
			managedPins[connection.String()] = revision
		}
		for _, connection := range missing {
			if _, resolved := managedPins[connection]; !resolved {
				return release.CandidateArtifactSet{}, candidateArtifactUnavailable(errors.New("managed-data candidate pin resolution returned incomplete result"))
			}
		}
	}
	if expected != nil {
		if expected.Artifact.SourceDigest != request.ArtifactDigest || expected.Artifact.ProjectDigest != compiledProject.Digest() || expected.Compiler.Graph.Digest() != compiledProject.Graph().Digest() || !reflect.DeepEqual(expected.Compiler.Plan, plan) || !reflect.DeepEqual(expected.Generation.ManagedDataPins, candidateManagedDataPins(managedPins)) {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("materialized compiler evidence differs from inspected plan evidence"))
		}
	}
	dataMode := release.GenerationDataRefreshSources
	dataRevision, err := candidateSourcesDataRevision(request.ArtifactDigest, managedPins)
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
	if expected != nil && !reflect.DeepEqual(expected.Extensions, extensions) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("materialized extension evidence differs from inspected evidence"))
	}
	if expected != nil && (expected.Generation.DataMode != dataMode || expected.Generation.DataRevision != dataRevision) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("materialized data mode differs from inspected plan evidence"))
	}
	stateInput := servingstate.CreateInput{ProjectID: projectID, Environment: environment, CreatedBy: request.OwnerID, Source: servingstate.SourceCandidate}
	var state servingstate.State
	if deterministic, ok := service.states.(interface {
		CreateWithID(context.Context, servingstate.ID, servingstate.CreateInput) (servingstate.State, error)
	}); ok {
		state, err = deterministic.CreateWithID(ctx, servingstate.ID("state-"+shortCandidateDigest(request.CandidateID)), stateInput)
	} else {
		state, err = service.states.Create(ctx, stateInput)
	}
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	var content bytes.Buffer
	_, expectedDigest, err := projectbundle.PackCompiledProject(compiledProject, plan, &content)
	if err != nil {
		_ = service.states.MarkFailed(ctx, state.ID, err)
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if _, err := service.artifacts.SaveUpload(ctx, state.ID, &content); err != nil {
		_ = service.states.MarkFailed(ctx, state.ID, err)
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	validated, err := service.validator.ValidateWithManagedDataRevisions(ctx, state.ID, managedPins)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	artifact, err := service.states.ArtifactByServingState(ctx, validated.ID)
	if err != nil || artifact.ID == "" || artifact.ServingStateID != validated.ID || artifact.Digest != validated.Digest {
		if err == nil {
			err = errors.New("validated serving artifact identity is incomplete")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if validated.Digest != expectedDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("candidate artifact digest changed during validation"))
	}
	identity, err := projectgraph.NewServingIdentity(projectID, string(environment), string(validated.ID))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	authorizationSnapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, compiledProject.Graph(), compiledProject.Manifest().Access)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	authorizationFingerprint, err := authorizationSnapshot.Digest()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	restrictions := candidateRestrictions(authorizationSnapshot)
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
	if expected != nil && (!reflect.DeepEqual(expected.Compiler.RelationExecution, relationExecution) || !reflect.DeepEqual(expected.Compiler.BaseRelationExecution, baseRelationExecution)) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("materialized relation execution evidence differs from inspected plan evidence"))
	}
	return release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: request.ArtifactDigest, ProjectDigest: compiledProject.Digest(), ContentDigest: validated.Digest, CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: compiledProject.Version()},
		Extensions:               extensions,
		AuthorizationFingerprint: authorizationFingerprint,
		Generation:               release.CandidateGenerationArtifact{Identity: identity, ServingArtifactID: artifact.ID, ArtifactDigest: validated.Digest, DataRevision: dataRevision, DataMode: dataMode, Deterministic: plan.Deterministic, ManagedDataPins: candidateManagedDataPins(managedPins), Connections: requirements, AuthoredConnections: authored, Restrictions: restrictions, BaseGateEvidence: base.gateEvidence},
		Compiler:                 release.CandidateCompilerEvidence{Graph: compiledProject.Graph(), Manifest: compiledProject.Manifest(), Plan: plan, Artifact: compiledProject, RelationExecution: relationExecution, BaseRelationExecution: baseRelationExecution},
	}, nil
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

func (service *candidateArtifactService) collectExtensionEvidence(ctx context.Context, requirements []string) ([]extension.Evidence, error) {
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

func (service *candidateArtifactService) generationBase(ctx context.Context, identity *projectgraph.ServingIdentity) (candidateGenerationBase, error) {
	if identity == nil {
		return candidateGenerationBase{pins: map[string]string{}}, nil
	}
	state, err := service.states.ByID(ctx, servingstate.ID(identity.GenerationID))
	if errors.Is(err, servingstate.ErrNotFound) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base generation not found"))
	}
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if state.ID != servingstate.ID(identity.GenerationID) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base generation identity mismatch"))
	}
	if state.ProjectID != identity.ProjectID || state.Environment != servingstate.Environment(identity.Environment) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base generation identity mismatch"))
	}
	if service.provenance == nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(errors.New("serving-state provenance is unavailable"))
	}
	baseProvenance, err := service.provenance.ProvenanceForServingState(ctx, *identity)
	if err != nil {
		if errors.Is(err, release.ErrNotFound) {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance not found"))
		}
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if err := baseProvenance.Validate(); err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance identity mismatch"))
	}
	if baseProvenance.Plan.Identity != *identity {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance identity mismatch"))
	}
	artifact, err := service.states.ArtifactByServingState(ctx, state.ID)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if artifact.Path == "" {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active project artifact path is empty"))
	}
	validation, err := projectbundle.ValidateArtifact(artifact.Path)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if validation.RootDir != "" {
		defer os.RemoveAll(validation.RootDir)
	}
	compiledBase, _, err := projectbundle.LoadCompiledProjectArtifact(validation.RootDir)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	baseArtifact, err := projectartifact.NewProject(compiledBase.Graph, compiledBase.Manifest)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if artifact.ServingStateID != state.ID || artifact.Digest == "" || artifact.Digest != state.Digest || artifact.Digest != validation.Digest || state.ProjectDigest == "" || state.ProjectDigest != validation.ProjectDigest || validation.ProjectID != identity.ProjectID.String() || artifact.ManifestJSON != state.ManifestJSON || validation.ManifestJSON != artifact.ManifestJSON || baseProvenance.Artifact.ContentDigest != artifact.Digest || baseProvenance.Artifact.ProjectDigest != state.ProjectDigest {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base generation content identity mismatch"))
	}
	pins := make(map[string]string, len(baseProvenance.Plan.ManagedDataPins))
	for _, pin := range baseProvenance.Plan.ManagedDataPins {
		connection, revision := pin.ConnectionID, pin.RevisionID
		if connection != strings.TrimSpace(connection) || revision != strings.TrimSpace(revision) || connection == "" || revision == "" {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains noncanonical managed-data pins"))
		}
		if _, exists := pins[connection]; exists {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains duplicate managed-data pins"))
		}
		pins[connection] = revision
	}
	dataRevision := strings.TrimSpace(baseProvenance.Plan.DataRevision)
	if dataRevision == "" && state.DuckLakeSnapshotID > 0 {
		dataRevision = fmt.Sprintf("snapshot:%d", state.DuckLakeSnapshotID)
	}
	baseBindings := make(map[string]string, len(baseProvenance.Plan.Bindings))
	for _, binding := range baseProvenance.Plan.Bindings {
		connectionID := strings.TrimSpace(binding.ConnectionID)
		kind := strings.TrimSpace(binding.ConnectorKind)
		if connectionID == "" || kind == "" {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains noncanonical binding evidence"))
		}
		if existing, ok := baseBindings[connectionID]; ok && existing != kind {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains conflicting binding evidence"))
		}
		baseBindings[connectionID] = kind
	}
	if len(baseBindings) == 0 {
		activations, activationErr := baseArtifact.ConnectionActivations()
		if activationErr != nil {
			return candidateGenerationBase{}, candidateArtifactInvalid(activationErr)
		}
		baseBindings = candidateActivationBindings(activations)
	}
	relationContext, err := candidateRelationContexts(pins, baseArtifact, baseBindings)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	return candidateGenerationBase{graph: validation.Graph, artifact: baseArtifact, pins: pins, bindings: baseBindings, snapshotID: state.DuckLakeSnapshotID, dataRevision: dataRevision, relationContext: relationContext, gateEvidence: baseProvenance.Plan.GateEvidence, active: true}, nil
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

// candidateSourcesDataRevision is release provenance for a source-refresh
// candidate. It includes the source artifact and the complete, sorted set of
// managed-data pins so the same candidate cannot silently race a pin change.
func candidateSourcesDataRevision(artifactDigest string, pins map[string]string) (string, error) {
	payload := struct {
		ArtifactDigest  string                   `json:"artifactDigest"`
		ManagedDataPins []release.ManagedDataPin `json:"managedDataPins"`
	}{ArtifactDigest: artifactDigest, ManagedDataPins: candidateManagedDataPins(pins)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode candidate source data revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sources:sha256:" + fmt.Sprintf("%x", digest[:]), nil
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
