package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddb "github.com/flidai/leapview/internal/dashboard/internal/db"
)

// Repository persists the dashboard authoring projection in the platform
// SQLite database. Queries are generated from the checked-in authoring SQL and
// transaction-scoped so CAS checks, immutable revision insertion, pointer
// updates, and command evidence commit as one unit.
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

var _ authoring.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" {
		return authoring.DashboardLifecycle{}, fmt.Errorf("workspace id is required")
	}
	if strings.TrimSpace(input.Lifecycle.WorkspaceID) != workspaceID {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle workspace does not match create workspace", authoring.ErrInvalidAuthoring)
	}
	if err := input.Lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Lifecycle.Status != authoring.LifecycleStatusDraft || input.Lifecycle.Draft == nil || input.Lifecycle.Published != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: dashboard create accepts draft lifecycle only", authoring.ErrInvalidAuthoring)
	}
	if err := input.Revision.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Revision.DashboardID != input.Lifecycle.ID {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: revision belongs to dashboard %q", authoring.ErrInvalidAuthoring, input.Revision.DashboardID)
	}
	if input.Lifecycle.SemanticModel != input.Revision.Document.SemanticModel {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle semantic model does not match initial revision", authoring.ErrInvalidAuthoring)
	}
	if input.Lifecycle.Title != input.Revision.Document.Title {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle title does not match initial revision", authoring.ErrInvalidAuthoring)
	}
	if !lifecycleReferencesRevision(input.Lifecycle, input.Revision.Token()) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: initial revision is not selected by lifecycle", authoring.ErrInvalidAuthoring)
	}
	documentJSON, provenanceJSON, err := encodeRevision(input.Revision)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}

	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	err = q.InsertAuthoringDashboard(ctx, dashboarddb.InsertAuthoringDashboardParams{WorkspaceID: workspaceID,
		DashboardID: string(input.Lifecycle.ID), OwnerPrincipalID: input.Lifecycle.OwnerPrincipalID,
		Slug: input.Lifecycle.Slug, Title: input.Lifecycle.Title, SemanticModel: input.Lifecycle.SemanticModel,
		Visibility: string(input.Lifecycle.Visibility), Status: string(input.Lifecycle.Status)})
	if err != nil {
		if isConstraint(err) {
			return authoring.DashboardLifecycle{}, fmt.Errorf("%w: dashboard identity already exists or slug is in use", authoring.ErrConflict)
		}
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertRevision(ctx, q, workspaceID, input.Revision, documentJSON, provenanceJSON); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Lifecycle.Draft != nil {
		if err := insertDraft(ctx, q, workspaceID, input.Lifecycle.ID, *input.Lifecycle.Draft); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, workspaceID, input.Lifecycle.ID)
}

func (r *Repository) Get(ctx context.Context, workspaceID string, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return authoring.DashboardLifecycle{}, fmt.Errorf("workspace id is required")
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.getLifecycle(ctx, dashboarddb.New(r.db), workspaceID, dashboardID)
}

func (r *Repository) List(ctx context.Context, workspaceID string) ([]authoring.DashboardLifecycle, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	rows, err := dashboarddb.New(r.db).ListAuthoringDashboards(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]authoring.DashboardLifecycle, 0, len(rows))
	for _, row := range rows {
		item, err := r.Get(ctx, workspaceID, authoring.DashboardID(row.DashboardID))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// CountBySemanticModel returns the non-archived authoring dashboard counts for
// each semantic model in deterministic semantic-model order.
func (r *Repository) CountBySemanticModel(ctx context.Context, workspaceID string) ([]authoring.SemanticModelUsage, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	rows, err := dashboarddb.New(r.db).CountAuthoringDashboardsBySemanticModel(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]authoring.SemanticModelUsage, 0, len(rows))
	for _, row := range rows {
		private, err := nonNegativeCount(row.PrivateCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q private count: %w", row.SemanticModel, err)
		}
		shared, err := nonNegativeCount(row.SharedCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q shared count: %w", row.SemanticModel, err)
		}
		total, err := nonNegativeCount(row.TotalCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q total count: %w", row.SemanticModel, err)
		}
		usage, err := authoring.NewSemanticModelUsage(row.SemanticModel, private, shared)
		if err != nil {
			return nil, err
		}
		if usage.Total != total {
			return nil, fmt.Errorf("%w: semantic model %q count buckets do not match total", authoring.ErrInvalidAuthoring, row.SemanticModel)
		}
		out = append(out, usage)
	}
	return out, nil
}

func nonNegativeCount(value int64) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("%w: count cannot be negative", authoring.ErrInvalidAuthoring)
	}
	return uint64(value), nil
}

func (r *Repository) GetRevision(ctx context.Context, workspaceID string, dashboardID authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return authoring.Revision{}, fmt.Errorf("workspace id is required")
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if err := revisionID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	return getRevision(ctx, dashboarddb.New(r.db), workspaceID, dashboardID, revisionID)
}

// LookupCommandResult returns durable idempotency evidence before a caller
// evaluates its expected revision. The fingerprint check belongs here (and in
// the transaction-scoped CAS methods below) so a reused command ID can never
// be mistaken for an optimistic-concurrency conflict after later edits.
func (r *Repository) LookupCommandResult(ctx context.Context, workspaceID string, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return authoring.CommandResult{}, false, fmt.Errorf("workspace id is required")
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.CommandResult{}, false, err
	}
	if err := evidence.Validate(); err != nil {
		return authoring.CommandResult{}, false, err
	}
	row, err := dashboarddb.New(r.db).GetAuthoringCommand(ctx, dashboarddb.GetAuthoringCommandParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), CommandID: string(evidence.ID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CommandResult{}, false, nil
	}
	if err != nil {
		return authoring.CommandResult{}, false, err
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	result := authoring.CommandResult{}
	if row.ResultRevisionID.Valid {
		result.Revision = authoring.RevisionToken{RevisionID: authoring.RevisionID(row.ResultRevisionID.String), Number: uint64(row.ResultRevisionNumber.Int64), ContentHash: row.ResultContentHash.String}
	}
	return result, true, nil
}

// GetPublishedCompilation retrieves the immutable compiler output selected by
// the current published pointer, preserving workspace/dashboard isolation.
func (r *Repository) GetPublishedCompilation(ctx context.Context, workspaceID string, dashboardID authoring.DashboardID) (authoring.CompiledRevision, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return authoring.CompiledRevision{}, fmt.Errorf("workspace id is required")
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.CompiledRevision{}, err
	}
	q := dashboarddb.New(r.db)
	published, err := q.GetAuthoringPublished(ctx, dashboarddb.GetAuthoringPublishedParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	row, err := q.GetAuthoringPublishedCompilation(ctx, dashboarddb.GetAuthoringPublishedCompilationParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), RevisionID: published.CompiledRevisionID, RevisionNumber: published.CompiledRevisionNumber, ContentHash: published.CompiledContentHash, DefinitionHash: published.CompiledDefinitionHash, SemanticServingStateID: published.CompiledSemanticServingStateID})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	var definition dashboarddefinition.Definition
	if err := json.Unmarshal([]byte(row.DefinitionJson), &definition); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("decode compiled dashboard definition: %w", err)
	}
	canonicalDefinition, err := json.Marshal(definition)
	if err != nil || string(canonicalDefinition) != row.DefinitionJson {
		return authoring.CompiledRevision{}, fmt.Errorf("%w: stored compiled definition is not canonical", authoring.ErrInvalidAuthoring)
	}
	compiledAt, err := parseTime(row.CompiledAt)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	compiled := authoring.CompiledRevision{WorkspaceID: row.WorkspaceID, DashboardID: authoring.DashboardID(row.DashboardID), AuthoredRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(row.RevisionID), Number: uint64(row.RevisionNumber), ContentHash: row.ContentHash}, Definition: definition, DefinitionHash: row.DefinitionHash, SemanticServingStateID: row.SemanticServingStateID, CompiledAt: compiledAt}
	if err := compiled.Validate(); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("validate stored compiled dashboard: %w", err)
	}
	if published.CompiledDefinitionHash != compiled.DefinitionHash || published.CompiledSemanticServingStateID != compiled.SemanticServingStateID || published.RevisionID != string(compiled.AuthoredRevision.RevisionID) || published.RevisionNumber != int64(compiled.AuthoredRevision.Number) || published.ContentHash != compiled.AuthoredRevision.ContentHash {
		return authoring.CompiledRevision{}, fmt.Errorf("%w: published compilation pointer does not match immutable compiled artifact", authoring.ErrInvalidAuthoring)
	}
	return compiled, nil
}

func (r *Repository) AppendDraft(ctx context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	workspaceID, err := validateAppendInput(input)
	if err != nil {
		return authoring.Revision{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.Revision{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, workspaceID, input.DashboardID, input.Evidence); err != nil {
		return authoring.Revision{}, err
	} else if replay != nil {
		revision, err := getRevision(ctx, q, workspaceID, input.DashboardID, replay.RevisionID)
		if err != nil {
			return authoring.Revision{}, err
		}
		return revision, nil
	}
	current, err := r.getLifecycle(ctx, q, workspaceID, input.DashboardID)
	if err != nil {
		return authoring.Revision{}, err
	}
	if current.Draft == nil {
		return authoring.Revision{}, conflict("dashboard has no draft pointer")
	}
	if current.Status == authoring.LifecycleStatusArchived {
		return authoring.Revision{}, conflict("archived dashboard cannot receive draft revisions")
	}
	if err := validateNextLifecycle(current, input.Next, input.Revision.Token()); err != nil {
		return authoring.Revision{}, err
	}
	if !sameToken(current.Draft.Revision, input.ExpectedDraftRevision) {
		return authoring.Revision{}, staleConflict()
	}
	if input.Revision.Number != current.Draft.Revision.Number+1 {
		return authoring.Revision{}, conflict("revision number is not the next draft revision")
	}
	documentJSON, provenanceJSON, err := encodeRevision(input.Revision)
	if err != nil {
		return authoring.Revision{}, err
	}
	if err := insertRevision(ctx, q, workspaceID, input.Revision, documentJSON, provenanceJSON); err != nil {
		return authoring.Revision{}, err
	}
	if _, err := q.UpdateAuthoringDashboard(ctx, dashboarddb.UpdateAuthoringDashboardParams{WorkspaceID: workspaceID, DashboardID: string(input.DashboardID), Slug: input.Next.Slug, Title: input.Next.Title, SemanticModel: input.Next.SemanticModel, Visibility: string(input.Next.Visibility), Status: string(input.Next.Status)}); err != nil {
		return authoring.Revision{}, err
	}
	nextDraftProvenance, err := json.Marshal(input.Next.Draft.Provenance)
	if err != nil {
		return authoring.Revision{}, err
	}
	result, err := q.UpdateAuthoringDraft(ctx, dashboarddb.UpdateAuthoringDraftParams{RevisionID: string(input.Revision.ID),
		RevisionNumber: int64(input.Revision.Number), ContentHash: input.Revision.ContentHash, ProvenanceJson: string(nextDraftProvenance),
		WorkspaceID: workspaceID, DashboardID: string(input.DashboardID), ExpectedRevisionID: string(input.ExpectedDraftRevision.RevisionID),
		ExpectedRevisionNumber: int64(input.ExpectedDraftRevision.Number), ExpectedContentHash: input.ExpectedDraftRevision.ContentHash})
	if err != nil {
		return authoring.Revision{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return authoring.Revision{}, staleConflict()
	}
	if err := insertCommand(ctx, q, workspaceID, input.DashboardID, input.Evidence, input.Revision.Token()); err != nil {
		return authoring.Revision{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoring.Revision{}, err
	}
	return input.Revision, nil
}

func (r *Repository) Publish(ctx context.Context, input authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	workspaceID, target, compilation, provenance, publishedAt, err := validatePublishInput(input)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, workspaceID, input.DashboardID, input.Evidence); err != nil {
		return authoring.DashboardLifecycle{}, err
	} else if replay != nil {
		return r.getLifecycle(ctx, q, workspaceID, input.DashboardID)
	}
	lifecycle, err := r.getLifecycle(ctx, q, workspaceID, input.DashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if lifecycle.Draft == nil || !sameToken(lifecycle.Draft.Revision, input.ExpectedDraftRevision) {
		return authoring.DashboardLifecycle{}, staleConflict()
	}
	if !sameToken(lifecycle.Draft.Revision, target) {
		return authoring.DashboardLifecycle{}, conflict("published revision must be the current draft revision")
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return authoring.DashboardLifecycle{}, conflict("archived dashboard cannot be published")
	}
	if lifecycle.Status != authoring.LifecycleStatusDraft && lifecycle.Status != authoring.LifecycleStatusPublished {
		return authoring.DashboardLifecycle{}, conflict("dashboard is not publishable")
	}
	revision, err := getRevision(ctx, q, workspaceID, input.DashboardID, target.RevisionID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if !sameToken(revision.Token(), target) {
		return authoring.DashboardLifecycle{}, conflict("published revision token does not match immutable revision")
	}
	if lifecycle.SemanticModel != revision.Document.SemanticModel || lifecycle.Title != revision.Document.Title {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle metadata does not match published revision", authoring.ErrInvalidAuthoring)
	}
	if compilation.Definition.ID != revision.Document.ID || compilation.Definition.Title != revision.Document.Title || compilation.Definition.SemanticModel != revision.Document.SemanticModel || compilation.Definition.SemanticModel != lifecycle.SemanticModel {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: compiled definition metadata does not match published revision", authoring.ErrInvalidAuthoring)
	}
	definitionJSON, err := json.Marshal(compilation.Definition)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertCompiledRevision(ctx, q, compilation, string(definitionJSON)); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	_, err = q.UpdateAuthoringDashboard(ctx, dashboarddb.UpdateAuthoringDashboardParams{WorkspaceID: workspaceID,
		DashboardID: string(input.DashboardID), Slug: lifecycle.Slug, Title: lifecycle.Title, SemanticModel: lifecycle.SemanticModel,
		Visibility: string(lifecycle.Visibility), Status: string(authoring.LifecycleStatusPublished)})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	err = q.UpsertAuthoringPublished(ctx, dashboarddb.UpsertAuthoringPublishedParams{WorkspaceID: workspaceID,
		DashboardID: string(input.DashboardID), RevisionID: string(target.RevisionID), RevisionNumber: int64(target.Number),
		ContentHash: target.ContentHash, CompiledRevisionID: string(compilation.AuthoredRevision.RevisionID), CompiledRevisionNumber: int64(compilation.AuthoredRevision.Number), CompiledContentHash: compilation.AuthoredRevision.ContentHash,
		CompiledDefinitionHash: compilation.DefinitionHash, CompiledSemanticServingStateID: compilation.SemanticServingStateID,
		ProvenanceJson: string(provenanceJSON), PublishedAt: formatTime(publishedAt)})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertCommand(ctx, q, workspaceID, input.DashboardID, input.Evidence, target); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, workspaceID, input.DashboardID)
}

func (r *Repository) Archive(ctx context.Context, input authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	workspaceID, err := validateArchiveInput(input)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, workspaceID, input.DashboardID, input.Evidence); err != nil {
		return authoring.DashboardLifecycle{}, err
	} else if replay != nil {
		return r.getLifecycle(ctx, q, workspaceID, input.DashboardID)
	}
	lifecycle, err := r.getLifecycle(ctx, q, workspaceID, input.DashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	expected := input.ExpectedCurrentRevision
	if lifecycle.Draft != nil {
		if !sameToken(lifecycle.Draft.Revision, expected) {
			return authoring.DashboardLifecycle{}, staleConflict()
		}
	} else if lifecycle.Published == nil || !sameToken(lifecycle.Published.Revision, expected) {
		return authoring.DashboardLifecycle{}, staleConflict()
	}
	if err := authoring.ValidateTransition(lifecycle.Status, authoring.LifecycleStatusArchived); err != nil {
		return authoring.DashboardLifecycle{}, conflict(err.Error())
	}
	result := expected
	_, err = q.ArchiveAuthoringDashboard(ctx, dashboarddb.ArchiveAuthoringDashboardParams{WorkspaceID: workspaceID, DashboardID: string(input.DashboardID)})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertCommand(ctx, q, workspaceID, input.DashboardID, input.Evidence, result); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, workspaceID, input.DashboardID)
}

type commandResult struct {
	RevisionID authoring.RevisionID
}

func commandReplay(ctx context.Context, q *dashboarddb.Queries, workspaceID string, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence) (*commandResult, error) {
	row, err := q.GetAuthoringCommand(ctx, dashboarddb.GetAuthoringCommandParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), CommandID: string(evidence.ID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return nil, authoring.ErrCommandReuse
	}
	if !row.ResultRevisionID.Valid || row.ResultRevisionID.String == "" {
		return &commandResult{}, nil
	}
	return &commandResult{RevisionID: authoring.RevisionID(row.ResultRevisionID.String)}, nil
}

func insertCompiledRevision(ctx context.Context, q *dashboarddb.Queries, compiled authoring.CompiledRevision, definitionJSON string) error {
	if err := compiled.Validate(); err != nil {
		return err
	}
	row, err := q.GetAuthoringPublishedCompilation(ctx, dashboarddb.GetAuthoringPublishedCompilationParams{WorkspaceID: compiled.WorkspaceID, DashboardID: string(compiled.DashboardID), RevisionID: string(compiled.AuthoredRevision.RevisionID), RevisionNumber: int64(compiled.AuthoredRevision.Number), ContentHash: compiled.AuthoredRevision.ContentHash, DefinitionHash: compiled.DefinitionHash, SemanticServingStateID: compiled.SemanticServingStateID})
	if err == nil {
		if row.DefinitionJson != definitionJSON || row.DefinitionHash != compiled.DefinitionHash || row.SemanticServingStateID != compiled.SemanticServingStateID {
			return conflict("compiled revision identity is immutable")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = q.InsertAuthoringCompiledRevision(ctx, dashboarddb.InsertAuthoringCompiledRevisionParams{WorkspaceID: compiled.WorkspaceID, DashboardID: string(compiled.DashboardID), RevisionID: string(compiled.AuthoredRevision.RevisionID), RevisionNumber: int64(compiled.AuthoredRevision.Number), ContentHash: compiled.AuthoredRevision.ContentHash, DefinitionJson: definitionJSON, DefinitionHash: compiled.DefinitionHash, SemanticServingStateID: compiled.SemanticServingStateID, CompiledAt: formatTime(compiled.CompiledAt)})
	if isConstraint(err) {
		return conflict("compiled revision identity is immutable")
	}
	return err
}

func insertCommand(ctx context.Context, q *dashboarddb.Queries, workspaceID string, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence, token authoring.RevisionToken) error {
	_, err := q.GetAuthoringCommand(ctx, dashboarddb.GetAuthoringCommandParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), CommandID: string(evidence.ID)})
	if err == nil {
		return authoring.ErrCommandReuse
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	provenanceJSON, err := json.Marshal(evidence.Provenance)
	if err != nil {
		return err
	}
	err = q.InsertAuthoringCommand(ctx, dashboarddb.InsertAuthoringCommandParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), CommandID: string(evidence.ID), RequestFingerprint: evidence.Fingerprint, Action: string(evidence.Action), ProvenanceJson: string(provenanceJSON), OccurredAt: formatTime(evidence.OccurredAt),
		ResultRevisionID: sql.NullString{String: string(token.RevisionID), Valid: true}, ResultRevisionNumber: sql.NullInt64{Int64: int64(token.Number), Valid: true}, ResultContentHash: sql.NullString{String: token.ContentHash, Valid: true}})
	if isConstraint(err) {
		return authoring.ErrCommandReuse
	}
	return err
}

func insertRevision(ctx context.Context, q *dashboarddb.Queries, workspaceID string, revision authoring.Revision, documentJSON, provenanceJSON string) error {
	err := q.InsertAuthoringRevision(ctx, dashboarddb.InsertAuthoringRevisionParams{WorkspaceID: workspaceID, DashboardID: string(revision.DashboardID), RevisionID: string(revision.ID), RevisionNumber: int64(revision.Number),
		DocumentJson: documentJSON, ContentHash: revision.ContentHash, ProvenanceJson: provenanceJSON, CreatedAt: formatTime(revision.CreatedAt)})
	if isConstraint(err) {
		return conflict("revision identity or number already exists")
	}
	return err
}

func insertDraft(ctx context.Context, q *dashboarddb.Queries, workspaceID string, dashboardID authoring.DashboardID, draft authoring.Draft) error {
	provenanceJSON, err := json.Marshal(draft.Provenance)
	if err != nil {
		return err
	}
	err = q.InsertAuthoringDraft(ctx, dashboarddb.InsertAuthoringDraftParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), DraftID: string(draft.ID), RevisionID: string(draft.Revision.RevisionID), RevisionNumber: int64(draft.Revision.Number),
		ContentHash: draft.Revision.ContentHash, ProvenanceJson: string(provenanceJSON)})
	if isConstraint(err) {
		return conflict("draft pointer is invalid")
	}
	return err
}

func (r *Repository) begin(ctx context.Context) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("dashboard authoring persistence is unavailable")
	}
	return r.db.BeginTx(ctx, nil)
}

func getRevision(ctx context.Context, q *dashboarddb.Queries, workspaceID string, dashboardID authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	row, err := q.GetAuthoringRevision(ctx, dashboarddb.GetAuthoringRevisionParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID), RevisionID: string(revisionID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.Revision{}, err
	}
	var document authoring.Dashboard
	if err := json.Unmarshal([]byte(row.DocumentJson), &document); err != nil {
		return authoring.Revision{}, fmt.Errorf("decode dashboard revision document: %w", err)
	}
	var provenance authoring.Provenance
	if err := json.Unmarshal([]byte(row.ProvenanceJson), &provenance); err != nil {
		return authoring.Revision{}, fmt.Errorf("decode dashboard revision provenance: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return authoring.Revision{}, err
	}
	revision := authoring.Revision{ID: authoring.RevisionID(row.RevisionID), DashboardID: authoring.DashboardID(row.DashboardID), Number: uint64(row.RevisionNumber), Document: document, ContentHash: row.ContentHash, Provenance: provenance, CreatedAt: createdAt}
	if err := revision.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate stored dashboard revision: %w", err)
	}
	canonical, err := json.Marshal(document)
	if err != nil || string(canonical) != row.DocumentJson {
		return authoring.Revision{}, fmt.Errorf("%w: stored dashboard document is not canonical", authoring.ErrInvalidAuthoring)
	}
	return revision, nil
}

func (r *Repository) getLifecycle(ctx context.Context, q *dashboarddb.Queries, workspaceID string, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	var lifecycle authoring.DashboardLifecycle
	identity, err := q.GetAuthoringDashboard(ctx, dashboarddb.GetAuthoringDashboardParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	lifecycle.ID = authoring.DashboardID(identity.DashboardID)
	lifecycle.WorkspaceID = identity.WorkspaceID
	lifecycle.OwnerPrincipalID = identity.OwnerPrincipalID
	lifecycle.Slug = identity.Slug
	lifecycle.Title = identity.Title
	lifecycle.SemanticModel = identity.SemanticModel
	lifecycle.Visibility = authoring.Visibility(identity.Visibility)
	lifecycle.Status = authoring.LifecycleStatus(identity.Status)
	draft, err := q.GetAuthoringDraft(ctx, dashboarddb.GetAuthoringDraftParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID)})
	if err == nil {
		var provenance authoring.Provenance
		if err := json.Unmarshal([]byte(draft.ProvenanceJson), &provenance); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		lifecycle.Draft = &authoring.Draft{ID: authoring.DraftID(draft.DraftID), DashboardID: dashboardID, Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(draft.RevisionID), Number: uint64(draft.RevisionNumber), ContentHash: draft.ContentHash}, Provenance: provenance}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return authoring.DashboardLifecycle{}, err
	}
	published, err := q.GetAuthoringPublished(ctx, dashboarddb.GetAuthoringPublishedParams{WorkspaceID: workspaceID, DashboardID: string(dashboardID)})
	if err == nil {
		var provenance authoring.Provenance
		if err := json.Unmarshal([]byte(published.ProvenanceJson), &provenance); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		at, err := parseTime(published.PublishedAt)
		if err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		lifecycle.Published = &authoring.Published{Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(published.RevisionID), Number: uint64(published.RevisionNumber), ContentHash: published.ContentHash}, Compilation: authoring.CompiledRevisionToken{AuthoredRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(published.CompiledRevisionID), Number: uint64(published.CompiledRevisionNumber), ContentHash: published.CompiledContentHash}, DefinitionHash: published.CompiledDefinitionHash, SemanticServingStateID: published.CompiledSemanticServingStateID}, PublishedAt: at, Provenance: provenance}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return authoring.DashboardLifecycle{}, err
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("validate stored dashboard lifecycle: %w", err)
	}
	return lifecycle, nil
}

func encodeRevision(revision authoring.Revision) (string, string, error) {
	document, err := json.Marshal(revision.Document)
	if err != nil {
		return "", "", err
	}
	provenance, err := json.Marshal(revision.Provenance)
	if err != nil {
		return "", "", err
	}
	return string(document), string(provenance), nil
}

func lifecycleReferencesRevision(lifecycle authoring.DashboardLifecycle, token authoring.RevisionToken) bool {
	return (lifecycle.Draft != nil && sameToken(lifecycle.Draft.Revision, token)) ||
		(lifecycle.Published != nil && sameToken(lifecycle.Published.Revision, token))
}

func validateNextLifecycle(current, next authoring.DashboardLifecycle, revision authoring.RevisionToken) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if next.WorkspaceID != current.WorkspaceID || next.OwnerPrincipalID != current.OwnerPrincipalID || next.ID != current.ID || next.Status != current.Status || next.Draft == nil || current.Draft == nil || next.Draft.ID != current.Draft.ID || !sameToken(next.Draft.Revision, revision) {
		return fmt.Errorf("%w: append lifecycle metadata must retain identity/status and point at the appended revision", authoring.ErrConflict)
	}
	if (current.Published == nil) != (next.Published == nil) {
		return fmt.Errorf("%w: append lifecycle metadata cannot change published pointer", authoring.ErrConflict)
	}
	if current.Published != nil && !sameToken(current.Published.Revision, next.Published.Revision) {
		return fmt.Errorf("%w: append lifecycle metadata cannot change published pointer", authoring.ErrConflict)
	}
	return nil
}

func sameToken(a, b authoring.RevisionToken) bool {
	return a.RevisionID == b.RevisionID && a.Number == b.Number && a.ContentHash == b.ContentHash
}

func validateAppendInput(input authoring.AppendDraftInput) (string, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" {
		return "", fmt.Errorf("workspace id is required")
	}
	if err := input.DashboardID.Validate(); err != nil {
		return "", err
	}
	if err := input.Evidence.Validate(); err != nil {
		return "", err
	}
	if input.Evidence.Action != authoring.AuthorizationActionEdit {
		return "", fmt.Errorf("%w: append requires edit command evidence", authoring.ErrInvalidAuthoring)
	}
	if err := input.ExpectedDraftRevision.ValidateComplete(); err != nil {
		return "", err
	}
	if err := input.Revision.Validate(); err != nil {
		return "", err
	}
	if input.Revision.DashboardID != input.DashboardID {
		return "", fmt.Errorf("%w: revision belongs to dashboard %q", authoring.ErrInvalidAuthoring, input.Revision.DashboardID)
	}
	if input.Next.WorkspaceID != workspaceID {
		return "", fmt.Errorf("%w: next lifecycle workspace does not match append workspace", authoring.ErrInvalidAuthoring)
	}
	if err := input.Next.Validate(); err != nil {
		return "", err
	}
	if input.Next.ID != input.DashboardID || input.Next.Draft == nil || !sameToken(input.Next.Draft.Revision, input.Revision.Token()) {
		return "", fmt.Errorf("%w: append requires a next lifecycle pointing at the appended revision", authoring.ErrInvalidAuthoring)
	}
	if input.Next.SemanticModel != input.Revision.Document.SemanticModel {
		return "", fmt.Errorf("%w: lifecycle semantic model does not match appended revision", authoring.ErrInvalidAuthoring)
	}
	if input.Next.Title != input.Revision.Document.Title {
		return "", fmt.Errorf("%w: lifecycle title does not match appended revision", authoring.ErrInvalidAuthoring)
	}
	return workspaceID, nil
}

func validatePublishInput(input authoring.PublishInput) (string, authoring.RevisionToken, authoring.CompiledRevision, authoring.Provenance, time.Time, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("workspace id is required")
	}
	if err := input.DashboardID.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if err := input.Evidence.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if input.Evidence.Action != authoring.AuthorizationActionPublish {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: publish requires publish command evidence", authoring.ErrInvalidAuthoring)
	}
	if err := input.ExpectedDraftRevision.ValidateComplete(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	target := input.Published.Revision
	if err := target.ValidateComplete(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if err := input.Published.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	compilation := input.Compilation
	if err := compilation.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if compilation.DashboardID != input.DashboardID || compilation.WorkspaceID != workspaceID {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: compiled revision scope does not match publish scope", authoring.ErrInvalidAuthoring)
	}
	if compilation.Token() != input.Published.Compilation || compilation.AuthoredRevision != target {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: published compilation token does not match compiled artifact", authoring.ErrInvalidAuthoring)
	}
	provenance := input.Published.Provenance
	if err := provenance.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if input.Published.PublishedAt.IsZero() {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: published timestamp is required", authoring.ErrInvalidAuthoring)
	}
	if input.Published.PublishedAt.Location() != time.UTC {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: published timestamp must be UTC", authoring.ErrInvalidAuthoring)
	}
	return workspaceID, target, compilation, provenance, input.Published.PublishedAt, nil
}

func validateArchiveInput(input authoring.ArchiveInput) (string, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" {
		return "", fmt.Errorf("workspace id is required")
	}
	if err := input.DashboardID.Validate(); err != nil {
		return "", err
	}
	if err := input.Evidence.Validate(); err != nil {
		return "", err
	}
	if input.Evidence.Action != authoring.AuthorizationActionArchive {
		return "", fmt.Errorf("%w: archive requires archive command evidence", authoring.ErrInvalidAuthoring)
	}
	if err := input.ExpectedCurrentRevision.ValidateComplete(); err != nil {
		return "", err
	}
	return workspaceID, nil
}

func staleConflict() error {
	return fmt.Errorf("%w: %w", authoring.ErrConflict, authoring.ErrStaleRevision)
}

func conflict(message string) error { return fmt.Errorf("%w: %s", authoring.ErrConflict, message) }

func isConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "constraint") || strings.Contains(strings.ToLower(err.Error()), "unique")
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid stored timestamp %q", authoring.ErrInvalidAuthoring, value)
	}
	return parsed.UTC(), nil
}
