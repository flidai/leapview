package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// AppendExplorationIntent is the closed, canonical idempotency input for an
// exploration append. It contains no converted visual or trusted binding
// map; those are produced only by a non-replay application preparer.
type AppendExplorationIntent struct {
	ProjectID        projectgraph.ResourceID
	ActorID          string
	DashboardID      authoring.DashboardID
	DraftID          authoring.DraftID
	ExpectedRevision authoring.RevisionToken
	PageID           string
	RequestID        string
	PlacementChoice  string
	Spec             exploration.ExplorationSpec
}

// AppendExplorationPreparation is returned only for a non-replayed intent.
// The service owns Release for the duration of the existing edit/CAS write.
type AppendExplorationPreparation struct {
	Command authoring.Command
	Release func()
}

// AppendExplorationPreparer is intentionally specialized to the closed
// append intent. It lets the application derive active-model bindings and
// candidate compilation after durable replay lookup, without changing the
// ordinary ExecuteValidated callback contract.
type AppendExplorationPreparer func(context.Context, AppendExplorationIntent, authoring.DashboardLifecycle) (AppendExplorationPreparation, error)

// AppendExploration authorizes the target and checks durable idempotency
// before current-draft, model, lease, or compiler admission. A non-replay
// preparation is then persisted through the same reducer-backed edit/CAS path
// used by ordinary authoring commands.
func (s *Service) AppendExploration(ctx context.Context, input AppendExplorationIntent, prepare AppendExplorationPreparer) (Result, error) {
	if s == nil || s.repository == nil || s.authorizer == nil {
		return Result{}, fmt.Errorf("dashboard authoring service is not configured")
	}
	if prepare == nil {
		return Result{}, fmt.Errorf("exploration append preparer is required")
	}
	if err := input.validateEnvelope(); err != nil {
		return Result{}, err
	}
	input.ProjectID = projectgraph.ResourceID(strings.TrimSpace(input.ProjectID.String()))
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.PageID = strings.TrimSpace(input.PageID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.PlacementChoice = strings.TrimSpace(input.PlacementChoice)
	projectID := input.ProjectID
	lifecycle, err := s.repository.Get(ctx, projectID, input.DashboardID)
	if err != nil {
		return Result{}, err
	}
	if lifecycle.ProjectID != projectID || lifecycle.ID != input.DashboardID {
		return Result{}, fmt.Errorf("dashboard lifecycle identity does not match append target")
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: input.ActorID, ProjectID: projectID, DashboardID: input.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return Result{}, err
	}
	fingerprint, err := appendExplorationFingerprint(input)
	if err != nil {
		return Result{}, err
	}
	evidenceAt, err := s.utcNow()
	if err != nil {
		return Result{}, err
	}
	evidence := authoring.CommandEvidence{
		ID: appendExplorationCommandID(input.RequestID), Fingerprint: fingerprint,
		Action:     authoring.AuthorizationActionEdit,
		Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: input.ActorID}, OccurredAt: evidenceAt,
	}
	if err := evidence.Validate(); err != nil {
		return Result{}, err
	}
	replay, found, err := s.repository.LookupCommandResult(ctx, projectID, input.DashboardID, evidence)
	if err != nil {
		return Result{}, err
	}
	if found {
		if replay.Revision.IsZero() {
			replay.Revision = currentToken(lifecycle)
		}
		return Result{Revision: replay.Revision, Lifecycle: lifecycle}, nil
	}
	prepared, err := prepare(ctx, input, lifecycle)
	if err != nil {
		return Result{}, err
	}
	if prepared.Release == nil {
		return Result{}, fmt.Errorf("exploration append preparation release is required")
	}
	defer prepared.Release()
	if err := validatePreparedAppend(input, lifecycle, evidence, prepared.Command); err != nil {
		return Result{}, err
	}
	return s.edit(ctx, projectID, prepared.Command, lifecycle, evidence)
}

func (input AppendExplorationIntent) validateEnvelope() error {
	if input.ProjectID == "" {
		return fmt.Errorf("project id is required")
	}
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project id is invalid: %w", err)
	}
	if strings.TrimSpace(input.ActorID) == "" || input.ActorID != strings.TrimSpace(input.ActorID) {
		return fmt.Errorf("actor id is required and cannot have surrounding whitespace")
	}
	if err := input.DashboardID.Validate(); err != nil {
		return err
	}
	if err := input.DraftID.Validate(); err != nil {
		return err
	}
	if err := input.ExpectedRevision.ValidateComplete(); err != nil {
		return err
	}
	if strings.TrimSpace(input.PageID) == "" || input.PageID != strings.TrimSpace(input.PageID) {
		return fmt.Errorf("page id is required and cannot have surrounding whitespace")
	}
	if strings.TrimSpace(input.RequestID) == "" || input.RequestID != strings.TrimSpace(input.RequestID) || len(input.RequestID) > 256 {
		return fmt.Errorf("request id is required and must be at most 256 characters")
	}
	switch strings.TrimSpace(input.PlacementChoice) {
	case "half", "full":
	default:
		return fmt.Errorf("unsupported exploration placement choice %q", input.PlacementChoice)
	}
	return nil
}

func appendExplorationFingerprint(input AppendExplorationIntent) (string, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("fingerprint exploration append intent: %w", err)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func appendExplorationCommandID(requestID string) authoring.CommandID {
	digest := sha256.Sum256([]byte(requestID))
	return authoring.CommandID("explore-" + hex.EncodeToString(digest[:16]))
}

func validatePreparedAppend(input AppendExplorationIntent, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence, command authoring.Command) error {
	if command.ID != evidence.ID || command.DashboardID != input.DashboardID || command.DraftID != input.DraftID || !sameToken(command.ExpectedRevision, input.ExpectedRevision) {
		return fmt.Errorf("exploration append preparation target does not match canonical intent")
	}
	if command.Provenance.Origin != authoring.OriginUI || command.Provenance.ActorID != input.ActorID {
		return fmt.Errorf("exploration append preparation provenance does not match canonical intent")
	}
	if command.AppendExplorationVisual == nil {
		return fmt.Errorf("exploration append preparation must contain an append visual payload")
	}
	if command.AppendExplorationVisual.PageID != input.PageID || command.AppendExplorationVisual.SemanticModel != lifecycle.SemanticModel.String() {
		return fmt.Errorf("exploration append preparation page or semantic model does not match target")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	return nil
}
