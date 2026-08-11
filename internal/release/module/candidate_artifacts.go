package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/platform/digest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatevalidate "github.com/flidai/leapview/internal/servingstate/validate"
	"github.com/flidai/leapview/internal/workspace"
)

type candidateArtifactService struct {
	states      ServingStateRepository
	workspaces  WorkspaceProvisioner
	artifacts   release.ArtifactStore
	validator   servingstatevalidate.Service
	environment servingstate.Environment
	pins        release.CandidatePinResolver
}

type candidateWorkspaceBase struct {
	graph      workspace.AssetGraph
	pins       map[string]string
	snapshotID int64
	active     bool
}

func (service *candidateArtifactService) Prepare(
	ctx context.Context,
	request release.CandidateArtifactRequest,
) (release.CandidateArtifactSet, error) {
	request.CandidateID = strings.TrimSpace(request.CandidateID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.OwnerID = strings.TrimSpace(request.OwnerID)
	request.Environment = strings.TrimSpace(request.Environment)
	request.ArtifactDigest = strings.TrimSpace(request.ArtifactDigest)
	request.Source.ProjectID = strings.TrimSpace(request.Source.ProjectID)
	request.Source.ArtifactDigest = strings.TrimSpace(request.Source.ArtifactDigest)
	request.Source.ProjectPath = strings.TrimSpace(request.Source.ProjectPath)
	request.Source.ProjectDigest = strings.TrimSpace(request.Source.ProjectDigest)
	request.Source.ProjectArtifactPath = strings.TrimSpace(
		request.Source.ProjectArtifactPath,
	)
	if service == nil || service.states == nil || service.workspaces == nil ||
		service.artifacts == nil || request.CandidateID == "" || request.ProjectID == "" ||
		request.OwnerID == "" || request.Environment == "" ||
		request.Source.ProjectArtifactPath == "" ||
		request.Source.ProjectID != request.ProjectID ||
		request.Source.ArtifactDigest != request.ArtifactDigest ||
		digest.ValidateSHA256Identity(request.ArtifactDigest) != nil ||
		digest.ValidateSHA256Identity(request.Source.ProjectDigest) != nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactInvalid
	}
	environment := servingstate.NormalizeEnvironment(servingstate.Environment(request.Environment))
	if environment != service.environment {
		return release.CandidateArtifactSet{}, fmt.Errorf(
			"%w: candidate environment does not match target",
			release.ErrCandidateArtifactInvalid,
		)
	}
	projectBytes, err := os.ReadFile(request.Source.ProjectArtifactPath)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	compiledProject, err := projectartifact.Decode(projectBytes)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if compiledProject.ID() != request.ProjectID ||
		compiledProject.Digest() != request.Source.ProjectDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(
			fmt.Errorf("retained project artifact does not match synchronized project"),
		)
	}
	workspaces := compiledProject.WorkspaceIDs()
	if len(workspaces) == 0 {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(
			fmt.Errorf("project has no workspaces"),
		)
	}

	result := release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{
			SourceDigest:    request.ArtifactDigest,
			ProjectDigest:   compiledProject.Digest(),
			CompilerVersion: projectartifact.CompilerVersion,
			SchemaVersion:   compiledProject.Version(),
			Workspaces: make(
				[]release.WorkspaceArtifactProvenance,
				0,
				len(workspaces),
			),
		},
		Workspaces: make([]release.CandidateArtifactWorkspace, 0, len(workspaces)),
	}
	policyHash := sha256.New()
	_, _ = fmt.Fprintf(policyHash, "%d:%s", len(request.OwnerID), request.OwnerID)
	for _, workspaceID := range workspaces {
		if err := ctx.Err(); err != nil {
			return release.CandidateArtifactSet{}, err
		}
		compiledWorkspace, ok := compiledProject.Workspace(workspaceID)
		if !ok {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(
				fmt.Errorf("project has no workspace %q", workspaceID),
			)
		}
		result.Artifact.Workspaces = append(
			result.Artifact.Workspaces,
			release.WorkspaceArtifactProvenance{
				WorkspaceID:    workspaceID,
				ArtifactDigest: compiledWorkspace.Digest(),
			},
		)
		if err := service.workspaces.Ensure(ctx, workspace.EnsureInput{
			ID: workspace.WorkspaceID(workspaceID), Title: workspaceID,
		}); err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
		}
		requirements, managedConnections, authoredConnections, err := candidateConnectionRequirements(
			compiledWorkspace,
		)
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		base, err := service.workspaceBase(ctx, request.ProjectID, workspaceID, environment)
		if err != nil {
			return release.CandidateArtifactSet{}, err
		}
		missingManaged := missingCandidateManagedConnections(
			managedConnections,
			base.pins,
		)
		if len(missingManaged) > 0 {
			if service.pins == nil {
				return release.CandidateArtifactSet{}, candidateArtifactUnavailable(
					fmt.Errorf("managed-data candidate pin resolution is unavailable"),
				)
			}
			resolved, resolveErr := service.pins.ResolveCandidatePins(
				ctx,
				request.ProjectID,
				missingManaged,
				string(environment),
			)
			if resolveErr != nil {
				return release.CandidateArtifactSet{}, candidateArtifactUnavailable(
					resolveErr,
				)
			}
			for connection, revision := range resolved {
				base.pins[connection] = revision
			}
		}
		workspacePlan, err := projectcompiler.PlanCompiledProjectAgainstGraph(
			compiledProject,
			workspaceID,
			base.graph,
		)
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		reuseSnapshot := base.active && base.snapshotID > 0 &&
			len(workspacePlan.Workspaces) == 1 &&
			!workspacePlan.Workspaces[0].Summary.MaterializationImpact
		if !reuseSnapshot && len(requirements) == 0 && len(base.pins) == 0 &&
			len(authoredConnections) == 0 {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(
				fmt.Errorf("workspace %q requires data preparation but has no refresh-capable connections", workspaceID),
			)
		}
		state, err := service.states.Create(ctx, servingstate.CreateInput{
			WorkspaceID: servingstate.WorkspaceID(workspaceID),
			ProjectID:   request.ProjectID, Environment: environment,
			CreatedBy: request.OwnerID, Source: servingstate.SourceCandidate,
		})
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
		}
		var content bytes.Buffer
		_, expectedDigest, err := projectbundle.PackCompiledProject(
			compiledProject,
			request.ArtifactDigest,
			projectbundle.PackProjectOptions{
				WorkspaceID: workspaceID, Environment: string(environment),
				ServingStateID: string(state.ID), ActiveGraph: base.graph,
				ManagedDataRevisions: base.pins,
			},
			&content,
		)
		if err != nil {
			_ = service.states.MarkFailed(ctx, state.ID, err)
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		if _, err := service.artifacts.SaveUpload(ctx, state.ID, &content); err != nil {
			_ = service.states.MarkFailed(ctx, state.ID, err)
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
		}
		validated, err := service.validator.Validate(ctx, state.ID)
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		if validated.Digest != expectedDigest {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(
				fmt.Errorf("candidate artifact digest changed during validation"),
			)
		}
		restrictions, err := candidateRestrictions(
			validated.AccessPolicyJSON,
			workspaceID,
			request.OwnerID,
		)
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		mode := "refresh_sources"
		dataRevision := "sources:" + request.ArtifactDigest
		connections := requirements
		authored := authoredConnections
		if reuseSnapshot {
			if err := service.states.RecordDuckLakeSnapshot(
				ctx,
				validated.ID,
				base.snapshotID,
			); err != nil {
				return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
			}
			mode = "reuse_snapshot"
			dataRevision = fmt.Sprintf("snapshot:%d", base.snapshotID)
			authored = nil
		}
		result.Workspaces = append(result.Workspaces, release.CandidateArtifactWorkspace{
			WorkspaceID: workspaceID, ServingStateID: string(validated.ID),
			ArtifactDigest: validated.Digest, DataRevision: dataRevision,
			DataMode:            mode,
			ManagedDataPins:     candidateManagedDataPins(base.pins),
			Connections:         connections,
			AuthoredConnections: authored,
			Restrictions:        restrictions,
		})
		_, _ = fmt.Fprintf(
			policyHash,
			"%d:%s:%d:%s",
			len(workspaceID),
			workspaceID,
			len(validated.AccessPolicyJSON),
			validated.AccessPolicyJSON,
		)
	}
	result.AuthorizationFingerprint = "sha256:" + hex.EncodeToString(policyHash.Sum(nil))
	return result, nil
}

func candidateRestrictions(policyJSON, workspaceID, ownerID string) ([]release.CandidateRestriction, error) {
	policy, err := accesssnapshot.Decode([]byte(policyJSON))
	if err != nil {
		return nil, fmt.Errorf("decode candidate access policy: %w", err)
	}
	names := make([]string, 0, len(policy.DataPolicies))
	for name := range policy.DataPolicies {
		names = append(names, name)
	}
	sort.Strings(names)
	restrictions := make([]release.CandidateRestriction, 0, len(names))
	for _, name := range names {
		item := policy.DataPolicies[name]
		applies, err := candidateSubjectApplies(policy, item.Subject, ownerID)
		if err != nil {
			return nil, fmt.Errorf("candidate data policy %q: %w", name, err)
		}
		if !applies {
			continue
		}
		objectID := "workspace:" + workspaceID
		if item.Object.Type != "workspace" {
			objectID = item.Object.Type + ":" + workspaceID + ":" + item.Object.ID
		}
		restrictions = append(restrictions, release.CandidateRestriction{
			ID: firstCandidateValue(item.ID, name), WorkspaceID: workspaceID,
			ObjectID: objectID, PolicyType: item.PolicyType,
			ExpressionJSON: item.ExpressionJSON,
		})
	}
	return restrictions, nil
}

func candidateSubjectApplies(
	policy accesssnapshot.AccessPolicy,
	subject accesssnapshot.Subject,
	ownerID string,
) (bool, error) {
	switch subject.Kind {
	case "":
		return true, nil
	case "principal", "service_principal":
		if subject.PrincipalID == "" {
			return false, fmt.Errorf("email-only subjects cannot be resolved in a private candidate")
		}
		return subject.PrincipalID == ownerID, nil
	case "group":
		group, ok := policy.Groups[subject.Group]
		if !ok {
			return false, fmt.Errorf("unknown group %q", subject.Group)
		}
		for _, member := range group.Members {
			if member.PrincipalID == ownerID {
				return true, nil
			}
			if member.PrincipalID == "" && member.Email != "" {
				return false, fmt.Errorf("email-only group members cannot be resolved in a private candidate")
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

func firstCandidateValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (service *candidateArtifactService) workspaceBase(
	ctx context.Context,
	projectID, workspaceID string,
	environment servingstate.Environment,
) (candidateWorkspaceBase, error) {
	state, artifact, err := service.states.ActiveArtifact(
		ctx,
		servingstate.WorkspaceID(workspaceID),
		environment,
	)
	if errors.Is(err, servingstate.ErrNotFound) {
		return candidateWorkspaceBase{
			graph: workspace.AssetGraph{}, pins: map[string]string{},
		}, nil
	}
	if err != nil {
		return candidateWorkspaceBase{}, candidateArtifactUnavailable(err)
	}
	if state.ProjectID != projectID || artifact.Path == "" {
		return candidateWorkspaceBase{}, candidateArtifactInvalid(
			fmt.Errorf("active workspace belongs to a different project"),
		)
	}
	validation, err := projectbundle.ValidateArtifactWithOptions(
		artifact.Path,
		workspaceID,
		string(state.ID),
		projectbundle.ValidateOptions{Environment: string(environment)},
	)
	if err != nil {
		return candidateWorkspaceBase{}, candidateArtifactUnavailable(err)
	}
	if validation.RootDir != "" {
		defer os.RemoveAll(validation.RootDir)
	}
	return candidateWorkspaceBase{
		graph: validation.Graph, pins: cloneCandidatePins(validation.ManagedDataRevisions),
		snapshotID: state.DuckLakeSnapshotID, active: true,
	}, nil
}

func candidateConnectionRequirements(
	compiled projectartifact.Workspace,
) ([]release.CandidateConnectionRequirement, []string, []release.CandidateAuthoredConnection, error) {
	activations, err := compiled.ConnectionActivations()
	if err != nil {
		return nil, nil, nil, err
	}
	requirements := make(
		[]release.CandidateConnectionRequirement,
		0,
		len(activations),
	)
	managed := make([]string, 0)
	authored := make([]release.CandidateAuthoredConnection, 0)
	for _, activation := range activations {
		switch activation.Mode {
		case projectartifact.ManagedActivation:
			managed = append(managed, activation.LogicalConnectionID)
		case projectartifact.AuthoredActivation:
			authored = append(authored, release.CandidateAuthoredConnection{
				LogicalConnectionID: activation.LogicalConnectionID,
				ConnectorKind:       activation.ConnectorKind,
			})
		case projectartifact.TargetBindingActivation:
			requirements = append(requirements, release.CandidateConnectionRequirement{
				LogicalConnectionID: activation.LogicalConnectionID,
				ConnectorKind:       activation.ConnectorKind,
			})
		default:
			return nil, nil, nil, fmt.Errorf(
				"compiled workspace connection %q has no activation mode",
				activation.LogicalConnectionID,
			)
		}
	}
	return requirements, managed, authored, nil
}

func cloneCandidatePins(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func candidateManagedDataPins(values map[string]string) []release.ManagedDataPin {
	connections := make([]string, 0, len(values))
	for connection := range values {
		connections = append(connections, connection)
	}
	sort.Strings(connections)
	result := make([]release.ManagedDataPin, 0, len(connections))
	for _, connection := range connections {
		result = append(result, release.ManagedDataPin{
			ConnectionID: connection,
			RevisionID:   values[connection],
		})
	}
	return result
}

func missingCandidateManagedConnections(
	connections []string,
	pins map[string]string,
) []string {
	result := make([]string, 0)
	for _, connection := range connections {
		if _, exists := pins[connection]; !exists {
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
