package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationdb "github.com/flidai/leapview/internal/dashboard/publication/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Tx = pgx.Tx
type AuditPort interface {
	RecordAuditIntent(context.Context, Tx, access.AuditIntent) error
}
type EventInput struct {
	EventID, ProjectID, PublicationID, ActorID, CorrelationID, Type, ServingStateID string
	Revision                                                                        int64
	Payload                                                                         []byte
}
type Event struct {
	EventID, ProjectID, PublicationID, ActorID, CorrelationID, Type, ServingStateID string
	Revision, AggregateVersion                                                      int64
	Payload                                                                         []byte
}
type publicationAuditEvidence struct {
	Operation, ResourceKind, ResourceID string
	Metadata                            []byte
}
type EventPort interface {
	AppendEvent(context.Context, Tx, EventInput) (Event, error)
}

var ErrUnavailable = errors.New("dashboard PostgreSQL publication store is unavailable")

type Repository struct {
	db     DBTX
	q      *publicationdb.Queries
	audit  AuditPort
	events EventPort
	native bool
}

func New(db DBTX, audit AuditPort, events EventPort) (*Repository, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	if _, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); !ok {
		return nil, fmt.Errorf("dashboard publication PostgreSQL handle must support transactions")
	}
	if audit == nil || events == nil {
		return nil, fmt.Errorf("dashboard publication PostgreSQL audit and event ports are required")
	}
	return &Repository{db: db, q: publicationdb.New(db), audit: audit, events: events, native: true}, nil
}
func NewRepository(db DBTX) *Repository { return &Repository{db: db, q: publicationdb.New(db)} }

// NewRepositoryWithAudit wires publication mutations to Access' narrow
// transaction-scoped audit-intent port. The recorder participates in the
// transaction opened by this repository and never commits or rolls it back.
func NewRepositoryWithAudit(db DBTX, audit AuditPort) *Repository {
	return &Repository{db: db, q: publicationdb.New(db), audit: audit}
}
func (r *Repository) IsNative() bool { return r != nil && r.native }

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrUnavailable
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

// publicationProjection is the typed shape shared by the full publication
// rows returned by sqlc's Get, GetByID, GetByPublicID, GetConfigured, List,
// and ListAll queries. Explicit adapters below make query-shape drift a
// compile-time error instead of silently zeroing fields through reflection.
type publicationProjection struct {
	ID                     string
	ProjectID              string
	Name                   string
	PublicID               string
	Dashboard              string
	DefaultPage            string
	ConfigurationDigest    string
	AllowedOriginsJSON     string
	DependencyAssetIDsJSON string
	Revision               int64
	Configured             bool
	ActiveServingStateID   *string
	SuspendedAt            pgtype.Timestamptz
	SuspendedBy            string
	ConfiguredAt           pgtype.Timestamptz
	DisabledAt             pgtype.Timestamptz
	RotatedAt              pgtype.Timestamptz
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func mapPublication(row publicationProjection) (publication.Publication, error) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(row.ProjectID))
	if err != nil {
		return publication.Publication{}, fmt.Errorf("decode publication project ID: %w", err)
	}
	out := publication.Publication{
		ID: row.ID, ProjectID: projectID, Name: row.Name,
		PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
		ConfigurationDigest: row.ConfigurationDigest, Configured: row.Configured,
		Revision: row.Revision, SuspendedBy: row.SuspendedBy,
		SuspendedAt: timestampValue(row.SuspendedAt), ConfiguredAt: timestampValue(row.ConfiguredAt),
		DisabledAt: timestampValue(row.DisabledAt), RotatedAt: timestampValue(row.RotatedAt),
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.ActiveServingStateID != nil {
		out.ServingStateID = strings.TrimSpace(*row.ActiveServingStateID)
	}
	if err := json.Unmarshal([]byte(row.AllowedOriginsJSON), &out.AllowedOrigins); err != nil {
		return publication.Publication{}, fmt.Errorf("decode publication origins: %w", err)
	}
	if err := json.Unmarshal([]byte(row.DependencyAssetIDsJSON), &out.DependencyAssetIDs); err != nil {
		return publication.Publication{}, fmt.Errorf("decode publication dependencies: %w", err)
	}
	return out, nil
}

func timestampValue(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}

func projectionFromGet(row publicationdb.GetRow) publicationProjection {
	return publicationProjection{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage, ConfigurationDigest: row.ConfigurationDigest, AllowedOriginsJSON: row.AllowedOriginsJson, DependencyAssetIDsJSON: row.DependencyAssetIdsJson, Revision: row.Revision, Configured: row.Configured, ActiveServingStateID: row.ActiveServingStateID, SuspendedAt: row.SuspendedAt, SuspendedBy: row.SuspendedBy, ConfiguredAt: row.ConfiguredAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func projectionFromByID(row publicationdb.GetByIDRow) publicationProjection {
	return publicationProjection{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage, ConfigurationDigest: row.ConfigurationDigest, AllowedOriginsJSON: row.AllowedOriginsJson, DependencyAssetIDsJSON: row.DependencyAssetIdsJson, Revision: row.Revision, Configured: row.Configured, ActiveServingStateID: row.ActiveServingStateID, SuspendedAt: row.SuspendedAt, SuspendedBy: row.SuspendedBy, ConfiguredAt: row.ConfiguredAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func projectionFromByPublicID(row publicationdb.GetByPublicIDRow) publicationProjection {
	return publicationProjection{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage, ConfigurationDigest: row.ConfigurationDigest, AllowedOriginsJSON: row.AllowedOriginsJson, DependencyAssetIDsJSON: row.DependencyAssetIdsJson, Revision: row.Revision, Configured: row.Configured, ActiveServingStateID: row.ActiveServingStateID, SuspendedAt: row.SuspendedAt, SuspendedBy: row.SuspendedBy, ConfiguredAt: row.ConfiguredAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func projectionFromList(row publicationdb.ListRow) publicationProjection {
	return publicationProjection{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage, ConfigurationDigest: row.ConfigurationDigest, AllowedOriginsJSON: row.AllowedOriginsJson, DependencyAssetIDsJSON: row.DependencyAssetIdsJson, Revision: row.Revision, Configured: row.Configured, ActiveServingStateID: row.ActiveServingStateID, SuspendedAt: row.SuspendedAt, SuspendedBy: row.SuspendedBy, ConfiguredAt: row.ConfiguredAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func projectionFromListAll(row publicationdb.ListAllRow) publicationProjection {
	return publicationProjection{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage, ConfigurationDigest: row.ConfigurationDigest, AllowedOriginsJSON: row.AllowedOriginsJson, DependencyAssetIDsJSON: row.DependencyAssetIdsJson, Revision: row.Revision, Configured: row.Configured, ActiveServingStateID: row.ActiveServingStateID, SuspendedAt: row.SuspendedAt, SuspendedBy: row.SuspendedBy, ConfiguredAt: row.ConfiguredAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func (r *Repository) Get(ctx context.Context, projectID projectgraph.ResourceID, name string) (publication.Publication, error) {
	row, err := r.q.Get(ctx, publicationdb.GetParams{
		ProjectID: strings.TrimSpace(projectID.String()),
		Name:      strings.TrimSpace(name),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return publication.Publication{}, publication.ErrNotFound
	}
	if err != nil {
		return publication.Publication{}, err
	}
	return mapPublication(projectionFromGet(row))
}

func (r *Repository) GetByPublicID(ctx context.Context, publicID string) (publication.Publication, error) {
	row, err := r.q.GetByPublicID(ctx, strings.TrimSpace(publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return publication.Publication{}, publication.ErrNotFound
	}
	if err != nil {
		return publication.Publication{}, err
	}
	return mapPublication(projectionFromByPublicID(row))
}

func (r *Repository) List(ctx context.Context, projectID projectgraph.ResourceID) ([]publication.Publication, error) {
	rows, err := r.q.List(ctx, strings.TrimSpace(projectID.String()))
	return mapListRows(rows, err)
}

func (r *Repository) ListAll(ctx context.Context) ([]publication.Publication, error) {
	rows, err := r.q.ListAll(ctx)
	return mapListAllRows(rows, err)
}

func (r *Repository) ListEvents(ctx context.Context, publicationID string) ([]publication.Event, error) {
	id, err := nativeUUID(publicationID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]publication.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, publication.Event{
			Type: row.EventType, ActorID: row.ActorID,
			ServingStateID: row.ServingStateID, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

func mapListRows(rows []publicationdb.ListRow, err error) ([]publication.Publication, error) {
	if err != nil {
		return nil, err
	}
	out := make([]publication.Publication, 0, len(rows))
	for _, row := range rows {
		mapped, err := mapPublication(projectionFromList(row))
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func mapListAllRows(rows []publicationdb.ListAllRow, err error) ([]publication.Publication, error) {
	if err != nil {
		return nil, err
	}
	out := make([]publication.Publication, 0, len(rows))
	for _, row := range rows {
		mapped, err := mapPublication(projectionFromListAll(row))
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (r *Repository) Suspend(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64) (publication.Publication, error) {
	if expectedRevision <= 0 {
		return publication.Publication{}, fmt.Errorf("publication expected revision must be positive")
	}
	return r.mutate(ctx, projectID, name, actorID, expectedRevision, "suspended", nil, func(q *publicationdb.Queries, event Event, row publication.Publication, evidence publicationAuditEvidence) (int64, error) {
		eventID, err := nativeUUID(event.EventID)
		if err != nil {
			return 0, err
		}
		return q.Suspend(ctx, publicationdb.SuspendParams{
			ActorID: strings.TrimSpace(actorID), ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name), ExpectedRevision: expectedRevision,
			DomainEventID: eventID, AggregateVersion: event.AggregateVersion, CorrelationID: event.CorrelationID, PayloadJson: event.Payload,
			AuditOperation: evidence.Operation, AuditResourceKind: evidence.ResourceKind, AuditResourceID: evidence.ResourceID, AuditMetadataJson: evidence.Metadata,
		})
	})
}

func (r *Repository) Resume(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64) (publication.Publication, error) {
	if expectedRevision <= 0 {
		return publication.Publication{}, fmt.Errorf("publication expected revision must be positive")
	}
	return r.mutate(ctx, projectID, name, actorID, expectedRevision, "resumed", nil, func(q *publicationdb.Queries, event Event, row publication.Publication, evidence publicationAuditEvidence) (int64, error) {
		eventID, err := nativeUUID(event.EventID)
		if err != nil {
			return 0, err
		}
		return q.Resume(ctx, publicationdb.ResumeParams{
			ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name), ExpectedRevision: expectedRevision, ActorID: strings.TrimSpace(actorID),
			DomainEventID: eventID, AggregateVersion: event.AggregateVersion, CorrelationID: event.CorrelationID, PayloadJson: event.Payload,
			AuditOperation: evidence.Operation, AuditResourceKind: evidence.ResourceKind, AuditResourceID: evidence.ResourceID, AuditMetadataJson: evidence.Metadata,
		})
	})
}

func (r *Repository) Rotate(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64) (publication.Publication, error) {
	if expectedRevision <= 0 {
		return publication.Publication{}, fmt.Errorf("publication expected revision must be positive")
	}
	publicID, err := newPublicID()
	if err != nil {
		return publication.Publication{}, err
	}
	return r.mutate(ctx, projectID, name, actorID, expectedRevision, "rotated", func(row *publication.Publication) error { row.PublicID = publicID; return nil }, func(q *publicationdb.Queries, event Event, row publication.Publication, evidence publicationAuditEvidence) (int64, error) {
		eventID, err := nativeUUID(event.EventID)
		if err != nil {
			return 0, err
		}
		return q.Rotate(ctx, publicationdb.RotateParams{
			PublicID: publicID, ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name), ExpectedRevision: expectedRevision, ActorID: strings.TrimSpace(actorID),
			DomainEventID: eventID, AggregateVersion: event.AggregateVersion, CorrelationID: event.CorrelationID, PayloadJson: event.Payload,
			AuditOperation: evidence.Operation, AuditResourceKind: evidence.ResourceKind, AuditResourceID: evidence.ResourceID, AuditMetadataJson: evidence.Metadata,
		})
	})
}

func (r *Repository) mutate(
	ctx context.Context,
	projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64, eventType string,
	adjust func(*publication.Publication) error,
	mutation func(*publicationdb.Queries, Event, publication.Publication, publicationAuditEvidence) (int64, error),
) (publication.Publication, error) {
	b, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return publication.Publication{}, ErrUnavailable
	}
	tx, err := b.Begin(ctxOrBackground(ctx))
	if err != nil {
		return publication.Publication{}, err
	}
	defer tx.Rollback(ctxOrBackground(ctx))
	q := r.q.WithTx(tx)
	stored, err := q.Get(ctx, publicationdb.GetParams{ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name)})
	if errors.Is(err, pgx.ErrNoRows) {
		return publication.Publication{}, publication.ErrNotFound
	}
	if err != nil {
		return publication.Publication{}, err
	}
	before, err := mapPublication(projectionFromGet(stored))
	if err != nil {
		return publication.Publication{}, err
	}
	if before.Revision != expectedRevision || ((eventType == "suspended" || eventType == "resumed" || eventType == "rotated") && !before.Configured) {
		if !before.Configured {
			return publication.Publication{}, publication.ErrConflict
		}
		return publication.Publication{}, apigenfailure.Wrap("precondition", publication.ErrConflict)
	}
	if expectedRevision == math.MaxInt64 {
		return publication.Publication{}, fmt.Errorf("%w: publication revision is exhausted", publication.ErrConflict)
	}
	row := before
	row.Revision = expectedRevision + 1
	switch eventType {
	case "suspended":
		row.SuspendedBy = strings.TrimSpace(actorID)
	case "resumed":
		row.SuspendedBy = ""
	}
	if adjust != nil {
		if err := adjust(&row); err != nil {
			return publication.Publication{}, err
		}
	}
	event, evidence, err := r.recordAuditIntent(ctx, tx, row.ID, &row, eventType, actorID)
	if err != nil {
		return publication.Publication{}, err
	}
	result, err := mutation(q, event, row, evidence)
	if err != nil {
		return publication.Publication{}, err
	}
	changed := result
	if changed == 0 {
		configured, err := q.GetConfiguredState(ctx, publicationdb.GetConfiguredStateParams{
			ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return publication.Publication{}, publication.ErrNotFound
		}
		if err != nil {
			return publication.Publication{}, err
		}
		if !configured {
			return publication.Publication{}, publication.ErrConflict
		}
		// A row that existed during the pre-read but no longer matches the
		// expected revision is the generated command's optimistic-concurrency
		// precondition failure, not a generic state conflict. Preserve the
		// publication conflict as the cause for callers that inspect domain
		// identity while classifying it for the public 412 contract.
		return publication.Publication{}, apigenfailure.Wrap("precondition", publication.ErrConflict)
	}
	if err := tx.Commit(ctxOrBackground(ctx)); err != nil {
		return publication.Publication{}, err
	}
	return r.Get(ctx, projectID, name)
}

func (r *Repository) recordAuditIntent(ctx context.Context, tx Tx, publicationID string, row *publication.Publication, eventType, actorID string) (Event, publicationAuditEvidence, error) {
	intent, ok := publication.AuditIntentFromContext(ctx)
	if !ok {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit intent is required")
	}
	if r.audit == nil {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit intent recorder is required")
	}
	if row == nil {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit intent publication is required")
	}
	if intent.ActorID == "" {
		intent.ActorID = strings.TrimSpace(actorID)
	} else if intent.ActorID != strings.TrimSpace(actorID) {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit actor does not match mutation actor")
	}
	qualifiedAction := "dashboard_publication." + strings.TrimSpace(eventType)
	if strings.TrimSpace(intent.Action) == "" {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit action is required")
	}
	if strings.TrimSpace(intent.Action) != qualifiedAction {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit action does not match mutation event")
	}
	eventType = qualifiedAction
	// Publication events are the source-owned aggregate sequence. Counting
	// after inserting the event gives each committed command a deterministic,
	// monotonic sequence while preserving the stable aggregate identity built
	// by the command producer.
	if r.events == nil {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication domain event port is required")
	}
	eventID, err := uuid.Parse(strings.TrimSpace(intent.EventID))
	if err != nil || eventID.Version() != 7 || eventID.String() != strings.ToLower(strings.TrimSpace(intent.EventID)) {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit event id must be a canonical UUIDv7")
	}
	payload, err := publicationEventPayload(*row, eventType)
	if err != nil {
		return Event{}, publicationAuditEvidence{}, err
	}
	event, err := r.events.AppendEvent(ctx, tx, EventInput{EventID: eventID.String(), ProjectID: row.ProjectID.String(), PublicationID: publicationID, ActorID: actorID, CorrelationID: intent.CorrelationID, Type: eventType, ServingStateID: row.ServingStateID, Revision: row.Revision, Payload: payload})
	if err != nil {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("append dashboard publication domain event: %w", err)
	}
	if event.EventID != eventID.String() || event.ProjectID != row.ProjectID.String() || event.PublicationID != publicationID || event.ActorID != actorID || event.CorrelationID != intent.CorrelationID || event.Type != eventType || event.ServingStateID != row.ServingStateID || event.Revision != row.Revision || event.AggregateVersion <= 0 || !canonicalJSONEqual(event.Payload, payload) {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication domain event returned mismatched identity")
	}
	intent.DomainEventID = event.EventID
	intent.AggregateSequence = event.AggregateVersion
	intent.EventID = eventID.String()
	if intent.ScopeID == "" {
		intent.ScopeID = row.ProjectID.String()
	}
	if intent.Source == "" {
		intent.Source = "dashboard.publication"
	}
	if intent.Operation == "" {
		intent.Operation = "mutation"
	}
	if intent.ResourceKind == "" {
		intent.ResourceKind = "publication"
	}
	if intent.ResourceID == "" {
		intent.ResourceID = row.ID
	}
	if intent.Outcome == "" {
		intent.Outcome = "success"
	}
	if intent.Capability == "" {
		intent.Capability = access.CapabilityResourcePublish
	}
	if intent.AggregateKey == "" {
		intent.AggregateKey = "dashboard_publication:" + row.ProjectID.String() + ":" + row.Name
	}
	canonicalIntent, err := intent.Canonicalize()
	if err != nil {
		return Event{}, publicationAuditEvidence{}, fmt.Errorf("dashboard publication audit intent is not canonical: %w", err)
	}
	intent = canonicalIntent
	evidence := publicationAuditEvidence{
		Operation: intent.Operation, ResourceKind: intent.ResourceKind, ResourceID: intent.ResourceID,
		Metadata: []byte(intent.MetadataJSON),
	}
	if err := r.audit.RecordAuditIntent(ctx, tx, intent); err != nil {
		return Event{}, publicationAuditEvidence{}, err
	}
	return event, evidence, nil
}

// ReconcileTx runs activation reconciliation against the caller-owned
// transaction. Production callers must provide this repository so every
// projection change is paired with the canonical event and audit ports.
func (r *Repository) ReconcileTx(
	ctx context.Context,
	tx Tx,
	input publication.ReconcileInput,
	activatePrincipal func(context.Context, Tx, projectgraph.ResourceID, string) error,
) error {
	if r == nil || r.events == nil || r.audit == nil {
		return fmt.Errorf("dashboard publication reconciliation event and audit ports are required")
	}
	if tx == nil {
		return ErrUnavailable
	}
	return reconcileTx(ctx, tx, input, activatePrincipal, r)
}

func reconcileTx(
	ctx context.Context,
	tx Tx,
	input publication.ReconcileInput,
	activatePrincipal func(context.Context, Tx, projectgraph.ResourceID, string) error,
	r *Repository,
) error {
	if tx == nil {
		return ErrUnavailable
	}
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("publication reconciliation requires project: %w", err)
	}
	input.ServingStateID = strings.TrimSpace(input.ServingStateID)
	if input.ServingStateID == "" {
		return fmt.Errorf("publication reconciliation requires serving state")
	}
	if len(input.Publications) > 0 && activatePrincipal == nil {
		return fmt.Errorf("publication reconciliation requires an Access principal activator")
	}
	if strings.TrimSpace(input.ActorID) == "" {
		return fmt.Errorf("publication reconciliation requires actor")
	}
	q := publicationdb.New(tx)
	rows, err := q.ListProjectStates(ctx, input.ProjectID.String())
	if err != nil {
		return err
	}
	type existingRow struct {
		id, name, digest, servingStateID string
		configured                       bool
		revision                         int64
	}
	existing := make(map[string]existingRow, len(rows))
	for _, row := range rows {
		existing[row.Name] = existingRow{
			id: row.ID, name: row.Name, digest: row.ConfigurationDigest, servingStateID: row.ActiveServingStateID, configured: row.Configured, revision: row.Revision,
		}
	}

	removals := make([]string, 0, len(existing))
	for name, row := range existing {
		if _, ok := input.Publications[name]; ok || !row.configured {
			continue
		}
		removals = append(removals, name)
	}
	sort.Strings(removals)
	for _, name := range removals {
		row := existing[name]
		if row.revision == math.MaxInt64 {
			return fmt.Errorf("%w: publication revision is exhausted", publication.ErrConflict)
		}
		rowID, err := nativeUUID(row.id)
		if err != nil {
			return err
		}
		stored, err := q.GetByID(ctx, rowID)
		if err != nil {
			return err
		}
		before, err := mapPublication(projectionFromByID(stored))
		if err != nil {
			return err
		}
		projection := before
		projection.Revision = row.revision + 1
		projection.Configured = false
		projection.ServingStateID = ""
		event, payload, evidence, err := r.appendReconcileEvent(ctx, tx, projection, "dashboard_publication.disabled", input.ActorID)
		if err != nil {
			return err
		}
		eventID, err := nativeUUID(event.EventID)
		if err != nil {
			return err
		}
		changed, err := q.Disable(ctx, publicationdb.DisableParams{ID: rowID, ExpectedRevision: row.revision, ActorID: input.ActorID, DomainEventID: eventID, AggregateVersion: event.AggregateVersion, CorrelationID: event.CorrelationID, PayloadJson: payload, AuditOperation: evidence.Operation, AuditResourceKind: evidence.ResourceKind, AuditResourceID: evidence.ResourceID, AuditMetadataJson: evidence.Metadata})
		if err != nil {
			return err
		}
		if changed != 1 {
			return publication.ErrConflict
		}
	}

	names := make([]string, 0, len(input.Publications))
	for name := range input.Publications {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		compiled := input.Publications[name]
		if err := activatePrincipal(ctx, tx, input.ProjectID, name); err != nil {
			return fmt.Errorf("reconcile publication principal %q: %w", name, err)
		}
		origins, err := json.Marshal(compiled.AllowedOrigins)
		if err != nil {
			return err
		}
		if compiled.AllowedOrigins == nil {
			origins = []byte("[]")
		}
		dependencies, err := json.Marshal(compiled.DependencyAssetIDs)
		if err != nil {
			return err
		}
		if compiled.DependencyAssetIDs == nil {
			dependencies = []byte("[]")
		}
		if current, ok := existing[name]; ok {
			eventType := ""
			if !current.configured {
				eventType = "configured"
			} else if current.digest != compiled.ConfigurationDigest {
				eventType = "configuration_changed"
			} else if current.servingStateID != input.ServingStateID {
				eventType = "serving_state_changed"
			}
			if eventType == "" {
				continue
			}
			if current.revision == math.MaxInt64 {
				return fmt.Errorf("%w: publication revision is exhausted", publication.ErrConflict)
			}
			currentID, err := nativeUUID(current.id)
			if err != nil {
				return err
			}
			stored, err := q.GetByID(ctx, currentID)
			if err != nil {
				return err
			}
			projection, err := mapPublication(projectionFromByID(stored))
			if err != nil {
				return err
			}
			projection.Revision = current.revision + 1
			projection.Dashboard = compiled.Dashboard
			projection.DefaultPage = compiled.DefaultPage
			projection.ConfigurationDigest = compiled.ConfigurationDigest
			projection.AllowedOrigins = append([]string(nil), compiled.AllowedOrigins...)
			projection.DependencyAssetIDs = append([]string(nil), compiled.DependencyAssetIDs...)
			projection.Configured = true
			projection.ServingStateID = input.ServingStateID
			if projection.AllowedOrigins == nil {
				projection.AllowedOrigins = []string{}
			}
			if projection.DependencyAssetIDs == nil {
				projection.DependencyAssetIDs = []string{}
			}
			fullEventType := "dashboard_publication." + eventType
			event, payload, evidence, err := r.appendReconcileEvent(ctx, tx, projection, fullEventType, input.ActorID)
			if err != nil {
				return err
			}
			eventID, err := nativeUUID(event.EventID)
			if err != nil {
				return err
			}
			changed, err := q.UpdateConfiguration(ctx, publicationdb.UpdateConfigurationParams{
				Dashboard: compiled.Dashboard, DefaultPage: compiled.DefaultPage,
				ConfigurationDigest: compiled.ConfigurationDigest,
				AllowedOriginsJson:  origins, DependencyAssetIdsJson: dependencies,
				ActiveServingStateID: input.ServingStateID,
				ID:                   currentID, ExpectedRevision: current.revision, ActorID: input.ActorID,
				DomainEventID: eventID, AggregateVersion: event.AggregateVersion, EventType: fullEventType, CorrelationID: event.CorrelationID, PayloadJson: payload,
				AuditOperation: evidence.Operation, AuditResourceKind: evidence.ResourceKind, AuditResourceID: evidence.ResourceID, AuditMetadataJson: evidence.Metadata,
			})
			if err != nil {
				return err
			}
			if changed != 1 {
				return publication.ErrConflict
			}
			continue
		}
		publicID, err := newPublicID()
		if err != nil {
			return err
		}
		idValue, err := uuid.NewV7()
		if err != nil {
			return err
		}
		id := idValue.String()
		idUUID, err := nativeUUID(id)
		if err != nil {
			return err
		}
		projection := publication.Publication{ID: id, ProjectID: input.ProjectID, Name: name, PublicID: publicID, Dashboard: compiled.Dashboard, DefaultPage: compiled.DefaultPage, ConfigurationDigest: compiled.ConfigurationDigest, Configured: true, Revision: 1, ServingStateID: input.ServingStateID, AllowedOrigins: compiled.AllowedOrigins, DependencyAssetIDs: compiled.DependencyAssetIDs}
		if projection.AllowedOrigins == nil {
			projection.AllowedOrigins = []string{}
		}
		if projection.DependencyAssetIDs == nil {
			projection.DependencyAssetIDs = []string{}
		}
		event, payload, evidence, err := r.appendReconcileEvent(ctx, tx, projection, "dashboard_publication.configured", input.ActorID)
		if err != nil {
			return err
		}
		eventID, err := nativeUUID(event.EventID)
		if err != nil {
			return err
		}
		created, err := q.Create(ctx, publicationdb.CreateParams{
			ID: idUUID, ProjectID: input.ProjectID.String(), Name: name, PublicID: publicID,
			Dashboard: compiled.Dashboard, DefaultPage: compiled.DefaultPage,
			ConfigurationDigest: compiled.ConfigurationDigest,
			AllowedOriginsJson:  origins, DependencyAssetIdsJson: dependencies,
			ActiveServingStateID: input.ServingStateID, ActorID: input.ActorID,
			DomainEventID: eventID, AggregateVersion: event.AggregateVersion, CorrelationID: event.CorrelationID, PayloadJson: payload,
			AuditOperation: evidence.Operation, AuditResourceKind: evidence.ResourceKind, AuditResourceID: evidence.ResourceID, AuditMetadataJson: evidence.Metadata,
		})
		if err != nil {
			return err
		}
		if created != 1 {
			return publication.ErrConflict
		}
	}
	return nil
}

func (r *Repository) appendReconcileEvent(ctx context.Context, tx Tx, row publication.Publication, eventType, actorID string) (Event, []byte, publicationAuditEvidence, error) {
	payload, err := publicationEventPayload(row, eventType)
	if err != nil {
		return Event{}, nil, publicationAuditEvidence{}, err
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return Event{}, nil, publicationAuditEvidence{}, err
	}
	event, err := r.events.AppendEvent(ctx, tx, EventInput{EventID: eventID.String(), ProjectID: row.ProjectID.String(), PublicationID: row.ID, ActorID: actorID, Type: eventType, ServingStateID: row.ServingStateID, Revision: row.Revision, Payload: payload})
	if err != nil {
		return Event{}, nil, publicationAuditEvidence{}, fmt.Errorf("append dashboard publication reconciliation event: %w", err)
	}
	if event.EventID != eventID.String() || event.ProjectID != row.ProjectID.String() || event.PublicationID != row.ID || event.ActorID != actorID || event.Type != eventType || event.ServingStateID != row.ServingStateID || event.Revision != row.Revision || event.AggregateVersion <= 0 || !canonicalJSONEqual(event.Payload, payload) {
		return Event{}, nil, publicationAuditEvidence{}, fmt.Errorf("dashboard publication reconciliation event returned mismatched identity")
	}
	intent := access.AuditIntent{
		EventID: event.EventID, DomainEventID: event.EventID,
		ActorID: actorID, ScopeID: row.ProjectID.String(), Source: "dashboard.publication", Operation: "reconcilePublications",
		Action: eventType, ResourceKind: "publication", ResourceID: row.ID, Capability: access.CapabilityResourcePublish,
		Outcome: "success", AggregateKey: "dashboard_publication:" + row.ProjectID.String() + ":" + row.Name,
		AggregateSequence: event.AggregateVersion, MetadataJSON: string(payload),
	}
	canonicalIntent, err := intent.Canonicalize()
	if err != nil {
		return Event{}, nil, publicationAuditEvidence{}, fmt.Errorf("dashboard publication reconciliation audit intent is not canonical: %w", err)
	}
	if err := r.audit.RecordAuditIntent(ctx, tx, canonicalIntent); err != nil {
		return Event{}, nil, publicationAuditEvidence{}, err
	}
	return event, payload, publicationAuditEvidence{Operation: canonicalIntent.Operation, ResourceKind: canonicalIntent.ResourceKind, ResourceID: canonicalIntent.ResourceID, Metadata: []byte(canonicalIntent.MetadataJSON)}, nil
}

func publicationEventPayload(row publication.Publication, eventType string) ([]byte, error) {
	return json.Marshal(struct {
		EventType           string   `json:"eventType"`
		PublicationID       string   `json:"publicationId"`
		ProjectID           string   `json:"projectId"`
		Name                string   `json:"name"`
		PublicID            string   `json:"publicId"`
		Dashboard           string   `json:"dashboard"`
		DefaultPage         string   `json:"defaultPage"`
		ConfigurationDigest string   `json:"configurationDigest"`
		AllowedOrigins      []string `json:"allowedOrigins"`
		DependencyAssetIDs  []string `json:"dependencyAssetIds"`
		Revision            int64    `json:"revision"`
		Configured          bool     `json:"configured"`
		ServingStateID      string   `json:"servingStateId"`
	}{eventType, row.ID, row.ProjectID.String(), row.Name, row.PublicID, row.Dashboard, row.DefaultPage, row.ConfigurationDigest, row.AllowedOrigins, row.DependencyAssetIDs, row.Revision, row.Configured, row.ServingStateID})
}

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

func newPublicID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func nativeUUID(value string) (pgtype.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID %q: %w", value, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
