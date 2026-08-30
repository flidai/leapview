package postgres

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddb "github.com/flidai/leapview/internal/dashboard/authoring/postgres/internal/db"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native pgx surface accepted by this capability. Mutations use
// one caller-owned pgx transaction and never open a second connection.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is an alias to pgx's transaction so generated sqlc leaves can be used
// directly while ownership of commit/rollback remains with this repository.
type Tx = pgx.Tx

// AuditPort is capability-neutral: production wires Access' PostgreSQL
// adapter while tests may provide a recorder. The source transaction is never
// committed by the port.
type AuditPort interface {
	RecordAuditIntent(context.Context, Tx, access.AuditIntent) error
}

// EventPort appends a canonical domain event in the same source transaction.
type EventPort interface {
	AppendEvent(context.Context, Tx, EventInput) (Event, error)
}
type GenerationFence interface {
	ValidateActiveGeneration(context.Context, Tx, graph.ServingIdentity) error
}

type EventInput struct {
	EventID, ProjectID, DashboardID, ActorID, CorrelationID string
	Revision                                                int64
	Type                                                    string
	Payload                                                 []byte
}
type Event struct {
	EventID, ProjectID, DashboardID, ActorID, CorrelationID string
	Revision, AggregateVersion                              int64
	Type                                                    string
	Payload                                                 []byte
}

// Repository persists the dashboard authoring projection in native PostgreSQL.
// Queries are generated from the checked-in authoring SQL and
// transaction-scoped so CAS checks, immutable revision insertion, pointer
// updates, and command evidence commit as one unit.
type Repository struct {
	db     DBTX
	audit  AuditPort
	events EventPort
	fence  GenerationFence
	native bool
}

// New constructs the production PostgreSQL authority. A generation fence is
// mandatory: revalidation must prove that the candidate serving generation is
// still active in the same transaction as its compilation pointer update.
func New(db DBTX, audit AuditPort, events EventPort, fence GenerationFence) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("dashboard authoring PostgreSQL database is required")
	}
	if _, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); !ok {
		return nil, fmt.Errorf("dashboard authoring PostgreSQL handle must support transactions")
	}
	if audit == nil || events == nil || fence == nil {
		return nil, fmt.Errorf("dashboard authoring PostgreSQL audit, event, and generation-fence ports are required")
	}
	return &Repository{db: db, audit: audit, events: events, fence: fence, native: true}, nil
}
func NewRepository(db DBTX) *Repository { return &Repository{db: db} }

// NewRepositoryWithAudit wires authoring mutations to Access' transaction-
// scoped audit-intent port. The recorder participates in the source
// transaction and never commits or rolls it back.
func NewRepositoryWithAudit(db DBTX, audit AuditPort) *Repository {
	return &Repository{db: db, audit: audit}
}

func (r *Repository) IsNative() bool { return r != nil && r.native }

// SchemaSQL returns the standalone schema for migration runners.
//
//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return fmt.Errorf("dashboard authoring PostgreSQL transaction is nil")
	}
	_, err := tx.Exec(ctxOrBackground(ctx), schemaSQL) // sqlc-exception: schema-ddl
	return err
}
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ authoring.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if input.Lifecycle.ProjectID.String() != projectID {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle project does not match create project", authoring.ErrInvalidAuthoring)
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
	if input.Lifecycle.SemanticModel.String() != input.Revision.Document.Spec.SemanticModel {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle semantic model does not match initial revision", authoring.ErrInvalidAuthoring)
	}
	if input.Lifecycle.Title != canonicalDocumentTitle(input.Revision.Document) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle title does not match initial revision", authoring.ErrInvalidAuthoring)
	}
	if !lifecycleReferencesRevision(input.Lifecycle, input.Revision.Token()) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: initial revision is not selected by lifecycle", authoring.ErrInvalidAuthoring)
	}
	if input.Operation.Enabled() {
		if err := validateCreateOperation(input.Operation, projectID, input.Lifecycle.ID, input.Revision.Token()); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	if err := validateNativeUUID(input.Lifecycle.OwnerPrincipalID, "owner principal id"); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := validateNativeUUIDv7Boundary(input.Revision.ID.String(), "revision id"); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Lifecycle.Draft != nil {
		if err := validateNativeUUIDv7Boundary(input.Lifecycle.Draft.ID.String(), "draft id"); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		if err := validateNativeUUIDv7Boundary(input.Lifecycle.Draft.Revision.RevisionID.String(), "draft revision id"); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	revisionNumber, err := checkedInt64(input.Revision.Number, "revision number")
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	documentJSON, provenanceJSON, err := encodeRevision(input.Revision)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}

	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback(ctx)
	q := dashboarddb.New(r.db).WithTx(tx)
	if input.Operation.Enabled() {
		if replay, found, err := lookupCreateOperation(ctx, tx, input.Operation); err != nil {
			return authoring.DashboardLifecycle{}, err
		} else if found {
			if replay.DashboardID == "" {
				return authoring.DashboardLifecycle{}, fmt.Errorf("%w: create operation has no dashboard result", authoring.ErrInvalidAuthoring)
			}
			lifecycle, err := r.getLifecycle(ctx, q, projectID, replay.DashboardID)
			if err != nil {
				return authoring.DashboardLifecycle{}, err
			}
			return lifecycle, nil
		}
	}
	ownerPrincipalID, err := nativeUUID(input.Lifecycle.OwnerPrincipalID)
	if err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("owner principal id: %w", err)
	}
	if !ownerPrincipalID.Valid {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: owner principal id must be a canonical UUID", authoring.ErrInvalidAuthoring)
	}
	var draftID pgtype.UUID
	var draftProvenance []byte
	if input.Lifecycle.Draft != nil {
		draftID = nativeUUIDValue(input.Lifecycle.Draft.ID.String())
		draftProvenance, err = json.Marshal(input.Lifecycle.Draft.Provenance)
		if err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	operation := input.Operation
	intent, _ := authoring.AuditIntentFromContext(ctx)
	applied, err := q.CreateDashboard(ctx, dashboarddb.CreateDashboardParams{
		ProjectID: projectID, DashboardID: string(input.Lifecycle.ID), OwnerPrincipalID: ownerPrincipalID,
		Slug: input.Lifecycle.Slug, Title: input.Lifecycle.Title, SemanticModel: input.Lifecycle.SemanticModel.String(),
		Visibility: string(input.Lifecycle.Visibility), Status: string(input.Lifecycle.Status),
		RevisionID: nativeUUIDValue(input.Revision.ID.String()), RevisionNumber: revisionNumber,
		DocumentJson: []byte(documentJSON), ContentHash: input.Revision.ContentHash, ProvenanceJson: []byte(provenanceJSON), CreatedAt: input.Revision.CreatedAt,
		DraftID: draftID, DraftProvenanceJson: draftProvenance, OperationEnabled: operation.Enabled(),
		ActorID: operation.ActorID, OperationKind: operation.Kind, IdempotencyKey: operation.IdempotencyKey,
		ConversationID: operation.ConversationID, ToolCallID: operation.ToolCallID, RequestFingerprint: operation.Fingerprint,
		EventID: nativeUUIDValue(intent.EventID),
	})
	if err != nil {
		if isConstraint(err) {
			return authoring.DashboardLifecycle{}, fmt.Errorf("%w: dashboard identity already exists or slug is in use", authoring.ErrConflict)
		}
		return authoring.DashboardLifecycle{}, err
	}
	if applied == 0 {
		replay, found, lookupErr := lookupCreateOperation(ctx, tx, input.Operation)
		if lookupErr != nil {
			return authoring.DashboardLifecycle{}, lookupErr
		}
		if !found {
			return authoring.DashboardLifecycle{}, authoring.ErrCommandReuse
		}
		lifecycle, getErr := r.getLifecycle(ctx, q, projectID, replay.DashboardID)
		if getErr != nil {
			return authoring.DashboardLifecycle{}, getErr
		}
		return lifecycle, nil
	}
	if err := r.recordAuditIntent(ctx, tx, input.Lifecycle, input.Revision, input.Operation.IdempotencyKey); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, input.ProjectID, input.Lifecycle.ID)
}

func (r *Repository) Get(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	lifecycle, err := r.getLifecycle(ctx, dashboarddb.New(r.db), projectID.String(), dashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	failure, ok, err := r.latestRevalidationFailure(ctx, projectID.String(), dashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if ok {
		lifecycle.Revalidation = &failure
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("validate stored dashboard lifecycle: %w", err)
	}
	return lifecycle, nil
}

func (r *Repository) List(ctx context.Context, projectID graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id is required: %w", err)
	}
	projectKey := projectID.String()
	rows, err := dashboarddb.New(r.db).ListDashboards(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	out := make([]authoring.DashboardLifecycle, 0, len(rows))
	for _, row := range rows {
		item, err := r.Get(ctx, projectID, authoring.DashboardID(row.DashboardID))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// CommitRevalidation atomically records immutable generation evidence and
// advances only the published compilation pointer whose authored revision and
// prior compiled identity still match. The active-generation check is inside
// the same transaction, so a concurrent activation cannot publish stale
// evidence.
func (r *Repository) CommitRevalidation(ctx context.Context, input authoring.RevalidationCommit) error {
	if err := validateRevalidationCommit(input); err != nil {
		return err
	}
	depsJSON, err := json.Marshal(input.DependencyIDs)
	if err != nil {
		return err
	}
	identityJSON, err := encodeServingIdentity(input.Generation.Identity)
	if err != nil {
		return err
	}
	priorIdentityJSON, err := encodeServingIdentity(input.PriorCompilation.SemanticIdentity)
	if err != nil {
		return err
	}
	compiledIdentityJSON, err := encodeServingIdentity(input.Compilation.SemanticIdentity)
	if err != nil {
		return err
	}
	definitionJSON, err := json.Marshal(input.Compilation.Definition)
	if err != nil {
		return err
	}
	for label, value := range map[string]string{
		"attempt id":                 input.AttemptID,
		"authored revision id":       input.AuthoredRevision.ID.String(),
		"compiled revision id":       input.Compilation.AuthoredRevision.RevisionID.String(),
		"published revision id":      input.Dashboard.Published.Revision.RevisionID.String(),
		"prior compiled revision id": input.PriorCompilation.AuthoredRevision.RevisionID.String(),
	} {
		if err := validateNativeUUIDv7Boundary(value, label); err != nil {
			return err
		}
	}
	publishedNumber, err := checkedInt64(input.Dashboard.Published.Revision.Number, "published revision number")
	if err != nil {
		return err
	}
	priorCompiledNumber, err := checkedInt64(input.PriorCompilation.AuthoredRevision.Number, "prior compiled revision number")
	if err != nil {
		return err
	}
	compiledNumber, err := checkedInt64(input.Compilation.AuthoredRevision.Number, "compiled revision number")
	if err != nil {
		return err
	}
	authoredNumber, err := checkedInt64(input.AuthoredRevision.Number, "authored revision number")
	if err != nil {
		return err
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := dashboarddb.New(tx)
	if r.fence == nil {
		return fmt.Errorf("dashboard authoring generation fence is required")
	}
	if err := r.fence.ValidateActiveGeneration(ctx, tx, input.Generation.Identity); err != nil {
		return err
	}
	current, err := q.GetPublished(ctx, dashboarddb.GetPublishedParams{ProjectID: input.Dashboard.ProjectID.String(), DashboardID: input.Dashboard.ID.String()})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.RevisionID != string(input.Dashboard.Published.Revision.RevisionID) || current.RevisionNumber != publishedNumber || current.ContentHash != input.Dashboard.Published.Revision.ContentHash || current.CompiledRevisionID != string(input.PriorCompilation.AuthoredRevision.RevisionID) || current.CompiledRevisionNumber != priorCompiledNumber || current.CompiledContentHash != input.PriorCompilation.AuthoredRevision.ContentHash || current.CompiledDefinitionHash != input.PriorCompilation.DefinitionHash || !canonicalJSONEqual([]byte(current.CompiledSemanticIdentityJson), []byte(priorIdentityJSON)) || current.CompiledSemanticModelID != input.PriorCompilation.SemanticModelID.String() {
		return authoring.ErrRevalidationConflict
	}
	result, err := q.CommitRevalidation(ctx, dashboarddb.CommitRevalidationParams{
		ProjectID: input.Dashboard.ProjectID.String(), DashboardID: input.Dashboard.ID.String(),
		RevisionID: nativeUUIDValue(input.Compilation.AuthoredRevision.RevisionID.String()), RevisionNumber: compiledNumber,
		ContentHash: input.Compilation.AuthoredRevision.ContentHash, DefinitionJson: definitionJSON, DefinitionHash: input.Compilation.DefinitionHash,
		SemanticModelID: input.Compilation.SemanticModelID.String(), SemanticIdentityJson: []byte(compiledIdentityJSON), CompiledAt: input.Compilation.CompiledAt,
		GenerationID: input.Generation.Identity.GenerationID, AttemptID: nativeUUIDValue(input.AttemptID), GenerationIdentityJson: []byte(identityJSON),
		GraphDigest: input.Generation.Graph.Digest(), DependencyIdsJson: []byte(depsJSON), AuthoredRevisionID: nativeUUIDValue(input.AuthoredRevision.ID.String()),
		AuthoredRevisionNumber: authoredNumber, AuthoredContentHash: input.AuthoredRevision.ContentHash, PriorCompiledIdentityJson: []byte(priorIdentityJSON),
		AttemptedAt: input.AttemptedAt, PriorCompiledRevisionID: nativeUUIDValue(input.PriorCompilation.AuthoredRevision.RevisionID.String()),
		PriorCompiledRevisionNumber: priorCompiledNumber, PriorCompiledContentHash: input.PriorCompilation.AuthoredRevision.ContentHash,
		PriorCompiledDefinitionHash: input.PriorCompilation.DefinitionHash, PriorCompiledSemanticModelID: input.PriorCompilation.SemanticModelID.String(),
	})
	if err != nil {
		if isConstraint(err) || strings.Contains(err.Error(), "compare-and-swap conflict") {
			return fmt.Errorf("%w: %v", authoring.ErrRevalidationConflict, err)
		}
		return err
	}
	if result != 1 {
		return authoring.ErrRevalidationConflict
	}
	return tx.Commit(ctx)
}

// RecordRevalidationFailure appends immutable failure evidence without
// changing published rows. Each retry supplies a new opaque attempt ID, so
// every diagnostic remains available for forensic inspection.
func (r *Repository) RecordRevalidationFailure(ctx context.Context, input authoring.RevalidationFailureInput) error {
	if err := validateRevalidationFailureInput(input); err != nil {
		return err
	}
	depsJSON, err := json.Marshal(input.DependencyIDs)
	if err != nil {
		return err
	}
	identityJSON, err := encodeServingIdentity(input.Generation.Identity)
	if err != nil {
		return err
	}
	priorIdentityJSON, err := encodeServingIdentity(input.PriorCompilation.SemanticIdentity)
	if err != nil {
		return err
	}
	for label, value := range map[string]string{
		"attempt id":           input.AttemptID,
		"authored revision id": input.AuthoredRevision.ID.String(),
	} {
		if err := validateNativeUUIDv7Boundary(value, label); err != nil {
			return err
		}
	}
	authoredNumber, err := checkedInt64(input.AuthoredRevision.Number, "authored revision number")
	if err != nil {
		return err
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := dashboarddb.New(tx)
	if r.fence == nil {
		return fmt.Errorf("dashboard authoring generation fence is required")
	}
	// Failure evidence is generation-bound just like a successful
	// revalidation. Validate the active serving identity on this exact source
	// transaction before inserting the immutable attempt, so a superseded
	// generation cannot leave diagnostics that look current.
	if err := r.fence.ValidateActiveGeneration(ctx, tx, input.Generation.Identity); err != nil {
		return err
	}
	errCode, errMessage := input.Failure.Code, input.Failure.Message
	result, err := q.RecordRevalidationFailure(ctx, dashboarddb.RecordRevalidationFailureParams{ProjectID: input.Dashboard.ProjectID.String(), DashboardID: input.Dashboard.ID.String(), GenerationID: input.Generation.Identity.GenerationID, AttemptID: nativeUUIDValue(input.AttemptID), GenerationIdentityJson: []byte(identityJSON), GraphDigest: input.Generation.Graph.Digest(), DependencyIdsJson: []byte(depsJSON), AuthoredRevisionID: nativeUUIDValue(input.AuthoredRevision.ID.String()), AuthoredRevisionNumber: authoredNumber, AuthoredContentHash: input.AuthoredRevision.ContentHash, PriorCompiledIdentityJson: []byte(priorIdentityJSON), ErrorCode: errCode, ErrorMessage: errMessage, AttemptedAt: input.Failure.FailedAt})
	if isConstraint(err) {
		return authoring.ErrRevalidationConflict
	}
	if err != nil {
		return err
	}
	if result != 1 {
		return authoring.ErrRevalidationConflict
	}
	return tx.Commit(ctx)
}

func validateRevalidationCommit(input authoring.RevalidationCommit) error {
	if err := authoring.ValidateRevalidationAttemptID(input.AttemptID); err != nil {
		return fmt.Errorf("%w: %v", authoring.ErrInvalidAuthoring, err)
	}
	if err := input.Generation.Validate(); err != nil {
		return err
	}
	if input.Dashboard.Published == nil || input.Dashboard.Status != authoring.LifecycleStatusPublished {
		return fmt.Errorf("%w: revalidation requires a published dashboard", authoring.ErrInvalidAuthoring)
	}
	if err := input.AuthoredRevision.Validate(); err != nil {
		return err
	}
	if err := input.Compilation.Validate(); err != nil {
		return err
	}
	if err := input.PriorCompilation.Validate(); err != nil {
		return err
	}
	if input.AttemptedAt.IsZero() || input.AttemptedAt.Location() != time.UTC {
		return fmt.Errorf("%w: revalidation attempt timestamp must be UTC", authoring.ErrInvalidAuthoring)
	}
	return nil
}

func validateRevalidationFailureInput(input authoring.RevalidationFailureInput) error {
	if err := authoring.ValidateRevalidationAttemptID(input.AttemptID); err != nil {
		return fmt.Errorf("%w: %v", authoring.ErrInvalidAuthoring, err)
	}
	if err := input.Generation.Validate(); err != nil {
		return err
	}
	if input.Dashboard.Published == nil || input.Dashboard.Status != authoring.LifecycleStatusPublished {
		return fmt.Errorf("%w: revalidation requires a published dashboard", authoring.ErrInvalidAuthoring)
	}
	if err := input.Failure.Validate(); err != nil {
		return err
	}
	return nil
}

// CountBySemanticModel returns the non-archived authoring dashboard counts for
// each semantic model in deterministic semantic-model order.
func (r *Repository) CountBySemanticModel(ctx context.Context, projectID graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id is required: %w", err)
	}
	rows, err := dashboarddb.New(r.db).CountBySemanticModel(ctx, projectID.String())
	if err != nil {
		return nil, err
	}
	out := make([]authoring.SemanticModelUsage, 0, len(rows))
	for _, row := range rows {
		private, err := nonNegativeCount(row.PrivateCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q private count: %w", row.SemanticModel, err)
		}
		organization, err := nonNegativeCount(row.OrganizationCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q organization count: %w", row.SemanticModel, err)
		}
		total, err := nonNegativeCount(row.TotalCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q total count: %w", row.SemanticModel, err)
		}
		usage, err := authoring.NewSemanticModelUsage(row.SemanticModel, private, organization)
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

func checkedInt64(value uint64, label string) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("%w: %s exceeds PostgreSQL bigint range", authoring.ErrInvalidAuthoring, label)
	}
	return int64(value), nil
}

func (r *Repository) GetRevision(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if err := revisionID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if err := validateNativeUUIDv7Boundary(string(revisionID), "revision id"); err != nil {
		return authoring.Revision{}, err
	}
	return getRevision(ctx, dashboarddb.New(r.db), projectID.String(), dashboardID, revisionID)
}

// LookupCommandResult returns durable idempotency evidence before a caller
// evaluates its expected revision. The fingerprint check belongs here (and in
// the transaction-scoped CAS methods below) so a reused command ID can never
// be mistaken for an optimistic-concurrency conflict after later edits.
func (r *Repository) LookupCommandResult(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.CommandResult{}, false, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.CommandResult{}, false, err
	}
	if err := evidence.Validate(); err != nil {
		return authoring.CommandResult{}, false, err
	}
	if err := validateNativeUUIDv7Boundary(string(evidence.ID), "command id"); err != nil {
		return authoring.CommandResult{}, false, err
	}
	row, err := dashboarddb.New(r.db).GetCommand(ctx, dashboarddb.GetCommandParams{ProjectID: projectID.String(), DashboardID: string(dashboardID), CommandID: nativeUUIDValue(string(evidence.ID))})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.CommandResult{}, false, nil
	}
	if err != nil {
		return authoring.CommandResult{}, false, err
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	result := authoring.CommandResult{}
	if row.ResultRevisionID != "" && row.ResultRevisionNumber != nil && row.ResultContentHash != nil {
		result.Revision = authoring.RevisionToken{RevisionID: authoring.RevisionID(row.ResultRevisionID), Number: uint64(*row.ResultRevisionNumber), ContentHash: *row.ResultContentHash}
	}
	return result, true, nil
}

// LookupCreateOperation returns the immutable result retained for a create or
// fork retry. It is intentionally independent of dashboard identity so a
// generated dashboard ID can be recovered after a process restart. The stored
// fingerprint is returned without comparison; the service authorizes the
// retained target before deciding whether a caller reused the key.
func (r *Repository) LookupCreateOperation(ctx context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	if !operation.Enabled() {
		return authoring.CreateOperationResult{}, false, nil
	}
	if err := validateCreateOperationKey(operation); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	row, err := dashboarddb.New(r.db).GetCreateOperation(ctx, dashboarddb.GetCreateOperationParams{ProjectID: operation.ProjectID.String(), ActorID: operation.ActorID, OperationKind: operation.Kind, IdempotencyKey: operation.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.CreateOperationResult{}, false, nil
	}
	if err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	fingerprint, dashboardID, revisionID, revisionNumber, contentHash := row.RequestFingerprint, row.DashboardID, row.ResultRevisionID, row.ResultRevisionNumber, row.ResultContentHash
	if revisionNumber <= 0 {
		return authoring.CreateOperationResult{}, false, fmt.Errorf("%w: create operation result revision is invalid", authoring.ErrInvalidAuthoring)
	}
	result := authoring.CreateOperationResult{DashboardID: authoring.DashboardID(dashboardID), Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(revisionID), Number: uint64(revisionNumber), ContentHash: contentHash}, Fingerprint: fingerprint}
	if err := authoring.ValidateDashboardID(result.DashboardID); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if err := result.Revision.Validate(); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	return result, true, nil
}

// GetPublishedCompilation retrieves the immutable compiler output selected by
// the current published pointer, preserving project/dashboard isolation.
func (r *Repository) GetPublishedCompilation(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID) (authoring.CompiledRevision, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.CompiledRevision{}, err
	}
	q := dashboarddb.New(r.db)
	projectKey := projectID.String()
	published, err := q.GetPublished(ctx, dashboarddb.GetPublishedParams{ProjectID: projectKey, DashboardID: string(dashboardID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	compiledRevisionID, err := nativeUUIDv7(published.CompiledRevisionID)
	if err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("stored compiled revision id: %w", err)
	}
	row, err := q.GetCompilation(ctx, dashboarddb.GetCompilationParams{ProjectID: projectKey, DashboardID: string(dashboardID), RevisionID: compiledRevisionID, RevisionNumber: published.CompiledRevisionNumber, ContentHash: published.CompiledContentHash, DefinitionHash: published.CompiledDefinitionHash, SemanticModelID: published.CompiledSemanticModelID, SemanticIdentityJson: []byte(published.CompiledSemanticIdentityJson)})
	if errors.Is(err, pgx.ErrNoRows) {
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
	if err != nil || !canonicalJSONEqual(canonicalDefinition, []byte(row.DefinitionJson)) {
		return authoring.CompiledRevision{}, fmt.Errorf("%w: stored compiled definition is not canonical", authoring.ErrInvalidAuthoring)
	}
	// pgx decodes timestamptz using the process-local location. Authoring
	// contracts require the canonical UTC location, so normalize at the SQL
	// boundary before validating the immutable artifact.
	compiledAt := row.CompiledAt.UTC()
	semanticIdentity, err := decodeServingIdentity(row.SemanticIdentityJson)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	compiled := authoring.CompiledRevision{ProjectID: graph.ResourceID(row.ProjectID), DashboardID: authoring.DashboardID(row.DashboardID), AuthoredRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(row.RevisionID), Number: uint64(row.RevisionNumber), ContentHash: row.ContentHash}, Definition: definition, DefinitionHash: row.DefinitionHash, SemanticModelID: graph.ResourceID(row.SemanticModelID), SemanticIdentity: semanticIdentity, CompiledAt: compiledAt}
	if err := compiled.Validate(); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("validate stored compiled dashboard: %w", err)
	}
	semanticJSON, err := encodeServingIdentity(compiled.SemanticIdentity)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	compiledNumber, err := checkedInt64(compiled.AuthoredRevision.Number, "compiled revision number")
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	if published.CompiledDefinitionHash != compiled.DefinitionHash || published.CompiledSemanticModelID != compiled.SemanticModelID.String() || !canonicalJSONEqual([]byte(published.CompiledSemanticIdentityJson), []byte(semanticJSON)) || published.RevisionID != string(compiled.AuthoredRevision.RevisionID) || published.RevisionNumber != compiledNumber || published.ContentHash != compiled.AuthoredRevision.ContentHash {
		return authoring.CompiledRevision{}, fmt.Errorf("%w: published compilation pointer does not match immutable compiled artifact", authoring.ErrInvalidAuthoring)
	}
	return compiled, nil
}

func (r *Repository) AppendDraft(ctx context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	projectID, err := validateAppendInput(input)
	if err != nil {
		return authoring.Revision{}, err
	}
	for label, value := range map[string]string{
		"command id":                 string(input.Evidence.ID),
		"revision id":                input.Revision.ID.String(),
		"expected draft revision id": input.ExpectedDraftRevision.RevisionID.String(),
	} {
		if err := validateNativeUUIDv7Boundary(value, label); err != nil {
			return authoring.Revision{}, err
		}
	}
	if err := validateNativeUUIDv7Boundary(input.Next.Draft.ID.String(), "draft id"); err != nil {
		return authoring.Revision{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.Revision{}, err
	}
	defer tx.Rollback(ctx)
	q := dashboarddb.New(r.db).WithTx(tx)
	// Serialize the command replay lookup with the owner-owned mutation
	// function. Without this read-side lock, a concurrent retry can observe
	// the post-CAS draft before its command row and incorrectly report stale
	// instead of returning the idempotent result.
	if _, lockErr := q.LockDashboard(ctx, dashboarddb.LockDashboardParams{ProjectID: projectID, DashboardID: string(input.DashboardID)}); lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
		return authoring.Revision{}, fmt.Errorf("lock dashboard for append replay: %w", lockErr)
	}
	if replay, err := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence); err != nil {
		return authoring.Revision{}, err
	} else if replay != nil {
		revision, err := getRevision(ctx, q, projectID, input.DashboardID, replay.RevisionID)
		if err != nil {
			return authoring.Revision{}, err
		}
		return revision, nil
	}
	current, err := r.getLifecycle(ctx, q, projectID, input.DashboardID)
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
	nextDraftProvenance, err := json.Marshal(input.Next.Draft.Provenance)
	if err != nil {
		return authoring.Revision{}, err
	}
	commandProvenanceJSON, err := json.Marshal(input.Evidence.Provenance)
	if err != nil {
		return authoring.Revision{}, err
	}
	revisionNumber, err := checkedInt64(input.Revision.Number, "revision number")
	if err != nil {
		return authoring.Revision{}, err
	}
	expectedRevisionNumber, err := checkedInt64(input.ExpectedDraftRevision.Number, "expected draft revision number")
	if err != nil {
		return authoring.Revision{}, err
	}
	result, err := q.AppendDraft(ctx, dashboarddb.AppendDraftParams{
		ProjectID: projectID, DashboardID: string(input.DashboardID), Slug: input.Next.Slug, Title: input.Next.Title,
		SemanticModel: input.Next.SemanticModel.String(), Visibility: string(input.Next.Visibility), Status: string(input.Next.Status),
		RevisionID: nativeUUIDValue(string(input.Revision.ID)), RevisionNumber: revisionNumber, DocumentJson: []byte(documentJSON),
		ContentHash: input.Revision.ContentHash, ProvenanceJson: []byte(provenanceJSON), CreatedAt: input.Revision.CreatedAt,
		DraftProvenanceJson: nextDraftProvenance, ExpectedRevisionID: nativeUUIDValue(string(input.ExpectedDraftRevision.RevisionID)),
		ExpectedRevisionNumber: expectedRevisionNumber, ExpectedContentHash: input.ExpectedDraftRevision.ContentHash,
		CommandID: nativeUUIDValue(string(input.Evidence.ID)), RequestFingerprint: input.Evidence.Fingerprint, Action: string(input.Evidence.Action),
		CommandProvenanceJson: commandProvenanceJSON, OccurredAt: input.Evidence.OccurredAt, EventID: auditEventID(ctx),
	})
	if err != nil {
		return authoring.Revision{}, err
	}
	if result == 0 {
		replay, replayErr := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence)
		if replayErr != nil {
			return authoring.Revision{}, replayErr
		}
		if replay == nil || replay.RevisionID == "" {
			return authoring.Revision{}, authoring.ErrCommandReuse
		}
		revision, revisionErr := getRevision(ctx, q, projectID, input.DashboardID, replay.RevisionID)
		if revisionErr != nil {
			return authoring.Revision{}, revisionErr
		}
		return revision, nil
	}
	if result != 1 {
		return authoring.Revision{}, staleConflict()
	}
	if err := r.recordAuditIntent(ctx, tx, input.Next, input.Revision, input.Evidence.ID.String()); err != nil {
		return authoring.Revision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return authoring.Revision{}, err
	}
	return input.Revision, nil
}

func (r *Repository) Publish(ctx context.Context, input authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	projectID, target, compilation, provenance, publishedAt, err := validatePublishInput(input)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	targetNumber, err := checkedInt64(target.Number, "published revision number")
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	for label, value := range map[string]string{
		"command id":                 string(input.Evidence.ID),
		"target revision id":         target.RevisionID.String(),
		"expected draft revision id": input.ExpectedDraftRevision.RevisionID.String(),
		"compiled revision id":       compilation.AuthoredRevision.RevisionID.String(),
	} {
		if err := validateNativeUUIDv7Boundary(value, label); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback(ctx)
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence); err != nil {
		return authoring.DashboardLifecycle{}, err
	} else if replay != nil {
		return r.getLifecycle(ctx, q, projectID, input.DashboardID)
	}
	lifecycle, err := r.getLifecycle(ctx, q, projectID, input.DashboardID)
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
	revision, err := getRevision(ctx, q, projectID, input.DashboardID, target.RevisionID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if !sameToken(revision.Token(), target) {
		return authoring.DashboardLifecycle{}, conflict("published revision token does not match immutable revision")
	}
	if lifecycle.SemanticModel.String() != revision.Document.Spec.SemanticModel || lifecycle.Title != canonicalDocumentTitle(revision.Document) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle metadata does not match published revision", authoring.ErrInvalidAuthoring)
	}
	if compilation.Definition.ID != revision.Document.Metadata.ID || compilation.Definition.Title != canonicalDocumentTitle(revision.Document) || compilation.Definition.SemanticModel != revision.Document.Spec.SemanticModel || compilation.Definition.SemanticModel != lifecycle.SemanticModel.String() {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: compiled definition metadata does not match published revision", authoring.ErrInvalidAuthoring)
	}
	definitionJSON, err := json.Marshal(compilation.Definition)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	semanticIdentityJSON, err := encodeServingIdentity(compilation.SemanticIdentity)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	commandProvenanceJSON, err := json.Marshal(input.Evidence.Provenance)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	applied, err := q.PublishDashboard(ctx, dashboarddb.PublishDashboardParams{
		ProjectID: projectID, DashboardID: string(input.DashboardID), Slug: lifecycle.Slug, Title: lifecycle.Title,
		SemanticModel: lifecycle.SemanticModel.String(), Visibility: string(lifecycle.Visibility), Status: string(authoring.LifecycleStatusPublished),
		RevisionID: nativeUUIDValue(string(target.RevisionID)), RevisionNumber: targetNumber, ContentHash: target.ContentHash,
		DefinitionJson: definitionJSON, DefinitionHash: compilation.DefinitionHash, SemanticModelID: compilation.SemanticModelID.String(),
		SemanticIdentityJson: []byte(semanticIdentityJSON), CompiledAt: compilation.CompiledAt, ProvenanceJson: provenanceJSON, PublishedAt: publishedAt,
		CommandID: nativeUUIDValue(string(input.Evidence.ID)), RequestFingerprint: input.Evidence.Fingerprint, Action: string(input.Evidence.Action),
		CommandProvenanceJson: commandProvenanceJSON, OccurredAt: input.Evidence.OccurredAt, EventID: auditEventID(ctx),
	})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if applied == 0 {
		return r.getLifecycle(ctx, q, projectID, input.DashboardID)
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, revision, input.Evidence.ID.String()); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, graph.ResourceID(projectID), input.DashboardID)
}

func (r *Repository) Archive(ctx context.Context, input authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	projectID, err := validateArchiveInput(input)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	expectedRevisionNumber, err := checkedInt64(input.ExpectedCurrentRevision.Number, "expected current revision number")
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	for label, value := range map[string]string{
		"command id":                   string(input.Evidence.ID),
		"expected current revision id": input.ExpectedCurrentRevision.RevisionID.String(),
	} {
		if err := validateNativeUUIDv7Boundary(value, label); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback(ctx)
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence); err != nil {
		return authoring.DashboardLifecycle{}, err
	} else if replay != nil {
		return r.getLifecycle(ctx, q, projectID, input.DashboardID)
	}
	lifecycle, err := r.getLifecycle(ctx, q, projectID, input.DashboardID)
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
	commandProvenanceJSON, err := json.Marshal(input.Evidence.Provenance)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	applied, err := q.ArchiveDashboard(ctx, dashboarddb.ArchiveDashboardParams{
		ProjectID: projectID, DashboardID: string(input.DashboardID), ExpectedRevisionID: nativeUUIDValue(string(expected.RevisionID)),
		ExpectedRevisionNumber: expectedRevisionNumber, ExpectedContentHash: expected.ContentHash,
		CommandID: nativeUUIDValue(string(input.Evidence.ID)), RequestFingerprint: input.Evidence.Fingerprint, Action: string(input.Evidence.Action),
		CommandProvenanceJson: commandProvenanceJSON, OccurredAt: input.Evidence.OccurredAt, EventID: auditEventID(ctx),
	})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if applied == 0 {
		return r.getLifecycle(ctx, q, projectID, input.DashboardID)
	}
	if applied != 1 {
		return authoring.DashboardLifecycle{}, staleConflict()
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, authoring.Revision{ID: expected.RevisionID, DashboardID: input.DashboardID, Number: expected.Number, ContentHash: expected.ContentHash}, input.Evidence.ID.String()); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, graph.ResourceID(projectID), input.DashboardID)
}

type commandResult struct {
	RevisionID authoring.RevisionID
}

func commandReplay(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence) (*commandResult, error) {
	row, err := q.GetCommand(ctx, dashboarddb.GetCommandParams{ProjectID: projectID, DashboardID: string(dashboardID), CommandID: nativeUUIDValue(string(evidence.ID))})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return nil, authoring.ErrCommandReuse
	}
	if row.ResultRevisionID == "" {
		return &commandResult{}, nil
	}
	return &commandResult{RevisionID: authoring.RevisionID(row.ResultRevisionID)}, nil
}

func validateCreateOperationKey(operation authoring.CreateOperation) error {
	return operation.Validate()
}

func validateCreateOperation(operation authoring.CreateOperation, projectID string, dashboardID authoring.DashboardID, token authoring.RevisionToken) error {
	if err := validateCreateOperationKey(operation); err != nil {
		return err
	}
	if operation.ProjectID.String() != projectID {
		return fmt.Errorf("%w: create operation project does not match create project", authoring.ErrInvalidAuthoring)
	}
	if err := dashboardID.Validate(); err != nil {
		return err
	}
	if err := token.Validate(); err != nil {
		return err
	}
	return nil
}

func lookupCreateOperation(ctx context.Context, tx Tx, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	row, err := dashboarddb.New(tx).GetCreateOperation(ctx, dashboarddb.GetCreateOperationParams{ProjectID: operation.ProjectID.String(), ActorID: operation.ActorID, OperationKind: operation.Kind, IdempotencyKey: operation.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.CreateOperationResult{}, false, nil
	}
	if err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if row.RequestFingerprint != operation.Fingerprint {
		return authoring.CreateOperationResult{}, false, authoring.ErrCommandReuse
	}
	result := authoring.CreateOperationResult{DashboardID: authoring.DashboardID(row.DashboardID), Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(row.ResultRevisionID), Number: uint64(row.ResultRevisionNumber), ContentHash: row.ResultContentHash}}
	if err := authoring.ValidateDashboardID(result.DashboardID); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if err := result.Revision.Validate(); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	return result, true, nil
}

func (r *Repository) begin(ctx context.Context) (Tx, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("dashboard authoring persistence is unavailable")
	}
	return r.beginTx(ctx)
}

func (r *Repository) beginTx(ctx context.Context) (Tx, error) {
	b, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("dashboard authoring PostgreSQL handle does not support transactions")
	}
	return b.Begin(ctxOrBackground(ctx))
}

// recordAuditIntent completes the transport-built intent with identities that
// only exist after the authoring mutation has been validated. Access owns
// aggregate sequence allocation; the immutable command identity, revision
// token, and dashboard aggregate make retries stable without copying authored
// documents or query content into audit metadata.
func (r *Repository) recordAuditIntent(ctx context.Context, tx Tx, lifecycle authoring.DashboardLifecycle, revision authoring.Revision, commandID string) error {
	intent, ok := authoring.AuditIntentFromContext(ctx)
	if !ok {
		return fmt.Errorf("dashboard authoring audit intent is required")
	}
	if r == nil || r.audit == nil {
		return fmt.Errorf("dashboard authoring audit intent recorder is required")
	}
	projectID := lifecycle.ProjectID.String()
	intent.ScopeID = projectID
	aggregateKey := "dashboard_authoring:" + projectID + ":" + lifecycle.ID.String()
	intent.ResourceKind = "dashboard"
	intent.ResourceID = lifecycle.ID.String()
	intent.AggregateKey = aggregateKey
	intent.AggregateSequence = 0
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return fmt.Errorf("dashboard authoring audit command identity is required")
	}
	revisionNumber, err := checkedInt64(revision.Number, "audit revision number")
	if err != nil {
		return err
	}
	actorID := strings.TrimSpace(intent.ActorID)
	if actorID == "" {
		actorID = strings.TrimSpace(intent.PrincipalID)
	}
	if actorID == "" {
		actorID = strings.TrimSpace(revision.Provenance.ActorID)
	}
	if actorID == "" {
		return fmt.Errorf("dashboard authoring audit actor is required")
	}
	intent.ActorID = actorID
	// The command boundary supplies one canonical UUIDv7 retry identity. It is
	// reused as the durable domain/audit event identity on every replay.
	auditID, parseErr := uuid.Parse(strings.TrimSpace(intent.EventID))
	if parseErr != nil || auditID.Version() != 7 || auditID.String() != strings.ToLower(strings.TrimSpace(intent.EventID)) {
		return fmt.Errorf("dashboard authoring audit event id must be a canonical UUIDv7")
	}
	intent.EventID = auditID.String()

	fields := map[string]any{
		"operationId": intent.Operation,
		"projectId":   projectID,
		"dashboardId": lifecycle.ID.String(),
	}
	// Archived published-only dashboards legitimately have no draft pointer.
	// Preserve draft metadata when it exists, and derive the origin from the
	// mutation revision first with lifecycle provenance as a safe fallback for
	// transitions (such as archive) that only carry a revision token.
	if lifecycle.Draft != nil {
		fields["draftId"] = lifecycle.Draft.ID.String()
	} else {
		fields["draftId"] = ""
	}
	if lifecycle.Draft != nil || lifecycle.Published != nil {
		origin := revision.Provenance.Origin
		if !origin.Valid() && lifecycle.Draft != nil {
			origin = lifecycle.Draft.Provenance.Origin
		}
		if !origin.Valid() && lifecycle.Published != nil {
			origin = lifecycle.Published.Provenance.Origin
		}
		if origin.Valid() {
			fields["origin"] = string(origin)
		}
	}
	metadata, err := access.RewriteGeneratedAuditEnvelopePayload(intent.MetadataJSON, fields)
	if err != nil {
		return fmt.Errorf("dashboard authoring audit metadata: %w", err)
	}
	intent.MetadataJSON = metadata
	if r.events == nil {
		return fmt.Errorf("dashboard authoring domain event port is required")
	}
	eventID := auditID
	event, err := r.events.AppendEvent(ctx, tx, EventInput{EventID: eventID.String(), ProjectID: projectID, DashboardID: lifecycle.ID.String(), ActorID: intent.ActorID, CorrelationID: intent.CorrelationID, Revision: revisionNumber, Type: intent.Action, Payload: []byte(metadata)})
	if err != nil {
		return fmt.Errorf("append dashboard authoring domain event: %w", err)
	}
	if event.EventID != eventID.String() || event.ProjectID != projectID || event.DashboardID != lifecycle.ID.String() || event.ActorID != intent.ActorID || event.CorrelationID != intent.CorrelationID || event.Revision != revisionNumber || event.Type != intent.Action || event.AggregateVersion <= 0 || !canonicalJSONEqual(event.Payload, []byte(metadata)) {
		return fmt.Errorf("dashboard authoring domain event returned mismatched identity")
	}
	intent.DomainEventID = event.EventID
	intent.AggregateSequence = event.AggregateVersion
	return r.audit.RecordAuditIntent(ctx, tx, intent)
}

func auditEventID(ctx context.Context) pgtype.UUID {
	intent, ok := authoring.AuditIntentFromContext(ctx)
	if !ok {
		return pgtype.UUID{}
	}
	return nativeUUIDValue(intent.EventID)
}

// canonicalJSONEqual compares event payloads as JSON values, rather than
// relying on object key order or whitespace chosen by a producer.
func canonicalJSONEqual(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	lb, err := json.Marshal(l)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(r)
	return err == nil && bytes.Equal(lb, rb)
}

func getRevision(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	row, err := q.GetRevision(ctx, dashboarddb.GetRevisionParams{ProjectID: projectID, DashboardID: string(dashboardID), RevisionID: nativeUUIDValue(string(revisionID))})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.Revision{}, err
	}
	var document document.DashboardDocument
	if err := json.Unmarshal([]byte(row.DocumentJson), &document); err != nil {
		return authoring.Revision{}, fmt.Errorf("decode dashboard revision document: %w", err)
	}
	var provenance authoring.Provenance
	if err := json.Unmarshal([]byte(row.ProvenanceJson), &provenance); err != nil {
		return authoring.Revision{}, fmt.Errorf("decode dashboard revision provenance: %w", err)
	}
	createdAt := row.CreatedAt.UTC()
	revision := authoring.Revision{ID: authoring.RevisionID(row.RevisionID), DashboardID: authoring.DashboardID(row.DashboardID), Number: uint64(row.RevisionNumber), Document: document, ContentHash: row.ContentHash, Provenance: provenance, CreatedAt: createdAt}
	if err := revision.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate stored dashboard revision: %w", err)
	}
	canonical, err := json.Marshal(document)
	if err != nil || !canonicalJSONEqual(canonical, []byte(row.DocumentJson)) {
		return authoring.Revision{}, fmt.Errorf("%w: stored dashboard document is not canonical", authoring.ErrInvalidAuthoring)
	}
	return revision, nil
}

func (r *Repository) getLifecycle(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	var lifecycle authoring.DashboardLifecycle
	identity, err := q.GetDashboard(ctx, dashboarddb.GetDashboardParams{ProjectID: projectID, DashboardID: string(dashboardID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	lifecycle.ID = authoring.DashboardID(identity.DashboardID)
	lifecycle.ProjectID = graph.ResourceID(identity.ProjectID)
	if !identity.OwnerPrincipalID.Valid {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: stored owner principal id is invalid", authoring.ErrInvalidAuthoring)
	}
	lifecycle.OwnerPrincipalID = uuid.UUID(identity.OwnerPrincipalID.Bytes).String()
	lifecycle.Slug = identity.Slug
	lifecycle.Title = identity.Title
	lifecycle.SemanticModel = graph.ResourceID(identity.SemanticModel)
	lifecycle.Visibility = authoring.Visibility(identity.Visibility)
	lifecycle.Status = authoring.LifecycleStatus(identity.Status)
	draft, err := q.GetDraft(ctx, dashboarddb.GetDraftParams{ProjectID: projectID, DashboardID: string(dashboardID)})
	if err == nil {
		var provenance authoring.Provenance
		if err := json.Unmarshal([]byte(draft.ProvenanceJson), &provenance); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		lifecycle.Draft = &authoring.Draft{ID: authoring.DraftID(draft.DraftID), DashboardID: dashboardID, Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(draft.RevisionID), Number: uint64(draft.RevisionNumber), ContentHash: draft.ContentHash}, Provenance: provenance}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return authoring.DashboardLifecycle{}, err
	}
	published, err := q.GetPublished(ctx, dashboarddb.GetPublishedParams{ProjectID: projectID, DashboardID: string(dashboardID)})
	if err == nil {
		var provenance authoring.Provenance
		if err := json.Unmarshal([]byte(published.ProvenanceJson), &provenance); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		at := published.PublishedAt.UTC()
		semanticIdentity, err := decodeServingIdentity(published.CompiledSemanticIdentityJson)
		if err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		lifecycle.Published = &authoring.Published{Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(published.RevisionID), Number: uint64(published.RevisionNumber), ContentHash: published.ContentHash}, Compilation: authoring.CompiledRevisionToken{AuthoredRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(published.CompiledRevisionID), Number: uint64(published.CompiledRevisionNumber), ContentHash: published.CompiledContentHash}, DefinitionHash: published.CompiledDefinitionHash, SemanticModelID: graph.ResourceID(published.CompiledSemanticModelID), SemanticIdentity: semanticIdentity}, PublishedAt: at, Provenance: provenance}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return authoring.DashboardLifecycle{}, err
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("validate stored dashboard lifecycle: %w", err)
	}
	return lifecycle, nil
}

func (r *Repository) latestRevalidationFailure(ctx context.Context, projectID string, dashboardID authoring.DashboardID) (authoring.RevalidationFailure, bool, error) {
	row, err := dashboarddb.New(r.db).LatestRevalidationFailure(ctx, dashboarddb.LatestRevalidationFailureParams{ProjectID: projectID, DashboardID: dashboardID.String()})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.RevalidationFailure{}, false, nil
	}
	if err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	if row.Status != "failed" {
		return authoring.RevalidationFailure{}, false, nil
	}
	identity, err := decodeServingIdentity(row.GenerationIdentityJson)
	if err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	var dependencyIDs []graph.ResourceID
	if err := json.Unmarshal([]byte(row.DependencyIdsJson), &dependencyIDs); err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	failure := authoring.RevalidationFailure{Identity: identity, DependencyIDs: dependencyIDs, Code: stringValue(row.ErrorCode), Message: stringValue(row.ErrorMessage), FailedAt: row.AttemptedAt}
	if err := failure.Validate(); err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	return failure, true, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

func encodeServingIdentity(identity graph.ServingIdentity) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", fmt.Errorf("%w: serving identity: %v", authoring.ErrInvalidAuthoring, err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode serving identity: %w", err)
	}
	return string(encoded), nil
}

func decodeServingIdentity(value string) (graph.ServingIdentity, error) {
	var identity graph.ServingIdentity
	if err := json.Unmarshal([]byte(value), &identity); err != nil {
		return graph.ServingIdentity{}, fmt.Errorf("decode serving identity: %w", err)
	}
	canonical, err := encodeServingIdentity(identity)
	if err != nil {
		return graph.ServingIdentity{}, err
	}
	if !canonicalJSONEqual([]byte(canonical), []byte(value)) {
		return graph.ServingIdentity{}, fmt.Errorf("%w: serving identity is not canonical", authoring.ErrInvalidAuthoring)
	}
	return identity, nil
}

func lifecycleReferencesRevision(lifecycle authoring.DashboardLifecycle, token authoring.RevisionToken) bool {
	return (lifecycle.Draft != nil && sameToken(lifecycle.Draft.Revision, token)) ||
		(lifecycle.Published != nil && sameToken(lifecycle.Published.Revision, token))
}

func canonicalDocumentTitle(value document.DashboardDocument) string {
	if value.Metadata.DisplayName != nil {
		return *value.Metadata.DisplayName
	}
	return value.Metadata.Name
}

func validateNextLifecycle(current, next authoring.DashboardLifecycle, revision authoring.RevisionToken) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if next.ProjectID != current.ProjectID || next.OwnerPrincipalID != current.OwnerPrincipalID || next.ID != current.ID || next.Status != current.Status || next.Draft == nil || current.Draft == nil || next.Draft.ID != current.Draft.ID || !sameToken(next.Draft.Revision, revision) {
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
	if err := input.ProjectID.Validate(); err != nil {
		return "", fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if err := authoring.ValidateDashboardID(input.DashboardID); err != nil {
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
	if input.Next.ProjectID.String() != projectID {
		return "", fmt.Errorf("%w: next lifecycle project does not match append project", authoring.ErrInvalidAuthoring)
	}
	if err := input.Next.Validate(); err != nil {
		return "", err
	}
	if input.Next.ID != input.DashboardID || input.Next.Draft == nil || !sameToken(input.Next.Draft.Revision, input.Revision.Token()) {
		return "", fmt.Errorf("%w: append requires a next lifecycle pointing at the appended revision", authoring.ErrInvalidAuthoring)
	}
	if input.Next.SemanticModel.String() != input.Revision.Document.Spec.SemanticModel {
		return "", fmt.Errorf("%w: lifecycle semantic model does not match appended revision", authoring.ErrInvalidAuthoring)
	}
	if input.Next.Title != canonicalDocumentTitle(input.Revision.Document) {
		return "", fmt.Errorf("%w: lifecycle title does not match appended revision", authoring.ErrInvalidAuthoring)
	}
	return projectID, nil
}

func validatePublishInput(input authoring.PublishInput) (string, authoring.RevisionToken, authoring.CompiledRevision, authoring.Provenance, time.Time, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if err := authoring.ValidateDashboardID(input.DashboardID); err != nil {
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
	if compilation.DashboardID != input.DashboardID || compilation.ProjectID.String() != projectID {
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
	return projectID, target, compilation, provenance, input.Published.PublishedAt, nil
}

func validateArchiveInput(input authoring.ArchiveInput) (string, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return "", fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if err := authoring.ValidateDashboardID(input.DashboardID); err != nil {
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
	return projectID, nil
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

// nativeUUID validates the UUID identity boundary before a value reaches a
// PostgreSQL UUID parameter. Native authoring IDs are UUIDv7; accepting an
// opaque domain identifier here would otherwise silently bind SQL NULL and
// turn malformed input into a misleading not-found/conflict result.
func nativeUUID(value string) (pgtype.UUID, error) {
	trimmed := strings.TrimSpace(value)
	id, err := uuid.Parse(trimmed)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: native identity %q is not a UUID", authoring.ErrInvalidAuthoring, trimmed)
	}
	if id == uuid.Nil {
		return pgtype.UUID{}, fmt.Errorf("%w: native identity %q is nil", authoring.ErrInvalidAuthoring, trimmed)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func nativeUUIDv7(value string) (pgtype.UUID, error) {
	id, err := nativeUUID(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if uuid.UUID(id.Bytes).Version() != 7 {
		return pgtype.UUID{}, fmt.Errorf("%w: native identity %q must be UUIDv7", authoring.ErrInvalidAuthoring, strings.TrimSpace(value))
	}
	return id, nil
}

// nativeUUIDValue is used only after the corresponding exported method has
// validated its caller/state identities. It deliberately returns an invalid
// zero UUID on impossible malformed state; sqlc then fails closed (rather
// than panicking the process or binding a parsed but wrong identity).
func nativeUUIDValue(value string) pgtype.UUID {
	id, err := nativeUUIDv7(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func validateNativeUUID(value, label string) error {
	if _, err := nativeUUID(value); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func validateNativeUUIDv7Boundary(value, label string) error {
	if _, err := nativeUUIDv7(value); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
