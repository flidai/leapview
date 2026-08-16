package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatevalidate "github.com/flidai/leapview/internal/servingstate/validate"
)

type candidateArtifactService struct {
	states      ServingStateRepository
	artifacts   release.ArtifactStore
	validator   servingstatevalidate.Service
	environment servingstate.Environment
	pins        ManagedDataPins
	provenance  release.ServingStateProvenanceRepository
}

type candidateGenerationBase struct {
	graph      projectgraph.ProjectGraph
	pins       map[string]string
	snapshotID int64
	active     bool
}

// Prepare creates exactly one project-generation serving artifact for one
// candidate target.
func (service *candidateArtifactService) Prepare(ctx context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	if request.CandidateID != strings.TrimSpace(request.CandidateID) || request.OwnerID != strings.TrimSpace(request.OwnerID) || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) || request.Source.ArtifactDigest != strings.TrimSpace(request.Source.ArtifactDigest) || request.Source.ProjectPath != strings.TrimSpace(request.Source.ProjectPath) || request.Source.ProjectDigest != strings.TrimSpace(request.Source.ProjectDigest) || request.Source.ProjectArtifactPath != strings.TrimSpace(request.Source.ProjectArtifactPath) {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	if service == nil || service.states == nil || service.artifacts == nil || request.CandidateID == "" || request.Scope.Validate() != nil || request.OwnerID == "" || request.Source.ProjectArtifactPath == "" || request.Source.ProjectID.Validate() != nil || request.Source.ProjectID != request.Scope.ProjectID || request.Source.ArtifactDigest != request.ArtifactDigest || platformdigest.ValidateSHA256Identity(request.ArtifactDigest) != nil || platformdigest.ValidateSHA256Identity(request.Source.ProjectDigest) != nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	if request.Scope.Environment != string(service.environment) {
		return release.CandidateArtifactSet{}, fmt.Errorf("%w: candidate environment does not match target", release.ErrCandidateArtifactInvalid)
	}
	projectBytes, err := os.ReadFile(request.Source.ProjectArtifactPath)
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
	plan, err := projectcompiler.PlanProject(request.Source.ProjectPath)
	if err != nil {
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
	if base.active {
		plan, err = projectcompiler.PlanProjectAgainstGraph(request.Source.ProjectPath, base.graph)
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
	}
	activations, err := compiledProject.ConnectionActivations()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	requirements, managed, authored, err := candidateConnectionRequirements(activations)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	missing := missingCandidateManagedConnections(managed, base.pins)
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
		resolved, resolveErr := service.pins.ResolveCandidatePins(ctx, projectID, missingIDs, string(environment))
		if resolveErr != nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(resolveErr)
		}
		for connection, revision := range resolved {
			base.pins[connection.String()] = revision
		}
	}
	dataMode := release.GenerationDataRefreshSources
	dataRevision := candidateSourcesDataRevision(request.ArtifactDigest, base.pins)
	if base.active && base.snapshotID > 0 && !plan.Summary.MaterializationImpact && base.graph.Validate() == nil {
		dataMode = release.GenerationDataReuseSnapshot
		dataRevision = fmt.Sprintf("snapshot:%d", base.snapshotID)
		authored = nil
	}
	if dataMode == release.GenerationDataRefreshSources && len(requirements) == 0 && len(base.pins) == 0 && len(authored) == 0 {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("project requires data preparation but has no refresh-capable connections"))
	}
	state, err := service.states.Create(ctx, servingstate.CreateInput{ProjectID: projectID, Environment: environment, CreatedBy: request.OwnerID, Source: servingstate.SourceCandidate})
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
	validated, err := service.validator.ValidateWithManagedDataRevisions(ctx, state.ID, base.pins)
	if err != nil {
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
	return release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: request.ArtifactDigest, ProjectDigest: compiledProject.Digest(), ContentDigest: validated.Digest, CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: compiledProject.Version()},
		AuthorizationFingerprint: authorizationFingerprint,
		Generation:               release.CandidateGenerationArtifact{Identity: identity, ArtifactDigest: validated.Digest, DataRevision: dataRevision, DataMode: dataMode, ManagedDataPins: candidateManagedDataPins(base.pins), Connections: requirements, AuthoredConnections: authored, Restrictions: restrictions},
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
	if err := baseProvenance.Validate(); err != nil || baseProvenance.Plan.Identity != *identity {
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
	return candidateGenerationBase{graph: validation.Graph, pins: pins, snapshotID: state.DuckLakeSnapshotID, active: true}, nil
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
			authored = append(authored, release.CandidateAuthoredConnection{ConnectionID: connectionID, ConnectorKind: activation.ConnectorKind})
		case projectartifact.TargetBindingActivation:
			requirements = append(requirements, release.CandidateConnectionRequirement{ConnectionID: connectionID, ConnectorKind: activation.ConnectorKind})
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

// candidateSourcesDataRevision is release provenance for a source-refresh
// candidate. It includes the source artifact and the complete, sorted set of
// managed-data pins so the same candidate cannot silently race a pin change.
func candidateSourcesDataRevision(artifactDigest string, pins map[string]string) string {
	payload := struct {
		ArtifactDigest  string                   `json:"artifactDigest"`
		ManagedDataPins []release.ManagedDataPin `json:"managedDataPins"`
	}{ArtifactDigest: artifactDigest, ManagedDataPins: candidateManagedDataPins(pins)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// The payload contains only strings and a concrete slice, so marshal
		// failure is impossible unless this shape changes incompatibly.
		panic("marshal candidate source data revision: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return "sources:sha256:" + fmt.Sprintf("%x", digest[:])
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
