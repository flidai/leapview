package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/servingstate"
)

type RefreshCandidateInput struct {
	Identity             projectgraph.ServingIdentity
	CreatedBy            string
	Active               ServingState
	ArtifactGraph        projectgraph.ProjectGraph
	ManagedDataRevisions map[string]string
}

func (s Service) Active(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) (ServingState, error) {
	state, artifact, err := s.ServingStates.ActiveArtifact(ctx, projectID, environment)
	if err != nil {
		return ServingState{}, err
	}
	return ServingState{State: state, Artifact: artifact}, nil
}

func (s Service) CreateRefreshCandidate(ctx context.Context, input RefreshCandidateInput) (ServingState, error) {
	if err := input.Identity.Validate(); err != nil {
		return ServingState{}, err
	}
	var accessPolicy projectmanifest.AccessPolicy
	if raw := strings.TrimSpace(input.Active.State.AccessPolicyJSON); raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &accessPolicy); err != nil {
			return ServingState{}, fmt.Errorf("decode active access policy: %w", err)
		}
	}
	if s.ServingStateMutations == nil {
		return ServingState{}, ErrServingStateMutationsRequired
	}
	created, err := s.ServingStateMutations.Create(ctx, servingstate.CreateInput{ProjectID: input.Identity.ProjectID, Environment: servingstate.Environment(input.Identity.Environment), CreatedBy: input.CreatedBy, Source: servingstate.SourceRefresh})
	if err != nil {
		return ServingState{}, err
	}
	candidateArtifact := servingstate.Artifact{ID: "artifact_" + string(created.ID), ServingStateID: created.ID, Digest: input.Active.Artifact.Digest, Format: input.Active.Artifact.Format, Path: input.Active.Artifact.Path, ManifestJSON: input.Active.Artifact.ManifestJSON, SizeBytes: input.Active.Artifact.SizeBytes, CreatedAt: input.Active.Artifact.CreatedAt}
	validation := servingstate.Validation{Digest: input.Active.State.Digest, ManifestJSON: input.Active.State.ManifestJSON, ProjectID: input.Identity.ProjectID, ProjectDigest: input.Active.State.ProjectDigest, AccessPolicy: accessPolicy, ManagedDataRevisions: cloneStringMap(input.ManagedDataRevisions), Graph: input.ArtifactGraph}
	for _, hook := range s.CandidateValidationHooks {
		if hook != nil {
			if err := hook.AfterArtifactValidation(ctx, created, validation); err != nil {
				_ = s.ServingStateMutations.MarkFailed(ctx, created.ID, err)
				return ServingState{}, err
			}
		}
	}
	validated, err := s.ServingStateMutations.SaveValidated(ctx, created.ID, validation, candidateArtifact)
	if err != nil {
		_ = s.ServingStateMutations.MarkFailed(ctx, created.ID, err)
		return ServingState{}, err
	}
	return ServingState{State: validated, Artifact: candidateArtifact}, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
func (s Service) RecordSnapshot(ctx context.Context, candidate ServingState, snapshotID int64) error {
	if snapshotID <= 0 {
		return fmt.Errorf("serving state snapshot id must be positive")
	}
	if s.ServingStateMutations == nil {
		return ErrServingStateMutationsRequired
	}
	return s.ServingStateMutations.RecordDuckLakeSnapshot(ctx, candidate.State.ID, snapshotID)
}
func (s Service) Activate(ctx context.Context, candidate ServingState) (servingstate.State, error) {
	identity := mustStateIdentity(candidate.State)
	if s.ServingStateMutations == nil {
		return servingstate.State{}, ErrServingStateMutationsRequired
	}
	return s.ServingStateMutations.Activate(ctx, identity.ProjectID, servingstate.Environment(identity.Environment), candidate.State.ID, "")
}
func (s Service) MarkFailed(ctx context.Context, state ServingState, cause error) error {
	if state.State.ID == "" || cause == nil {
		return nil
	}
	if s.ServingStateMutations == nil {
		return ErrServingStateMutationsRequired
	}
	return s.ServingStateMutations.MarkFailed(ctx, state.State.ID, cause)
}
