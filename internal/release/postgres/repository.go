// Package postgres implements the clean-slate PostgreSQL release authority.
//
// Release persistence is deliberately native pgx.  A caller-owned transaction
// is the composition boundary for state, audit, and durable domain events;
// this package never opens a second control store or uses SQLite workflow and
// outbox projections.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
	publicjobs "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native pgx connection surface accepted by read methods and
// transaction wrappers.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is intentionally strict: it can only be satisfied by a caller-owned
// PostgreSQL transaction, never a pool or connection. Methods never commit or
// roll back this value.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// AuditAppender is the Release-owned audit boundary. Composition adapters map
// it to the Access authority while receiving this package's caller-owned pgx
// transaction, and therefore share its commit.
type AuditAppender interface {
	RecordAuditEvent(context.Context, Tx, access.AuditIntent) (AuditEvent, error)
}

// EventAppender is the durable event-log boundary. The release repository
// passes its caller-owned transaction through unchanged.
type EventAppender interface {
	AppendEvent(context.Context, Tx, EventInput) (Event, error)
}

// WorkflowAppender is the Release-owned workflow boundary. Composition maps
// this narrow port to the jobs PostgreSQL authority; the caller-owned pgx
// transaction remains the single commit/rollback boundary.
type WorkflowAppender interface {
	RecordWorkflow(context.Context, Tx, publicjobs.WorkflowIntent) error
}

// AuditEvent is the release-owned projection returned by a composition audit
// adapter. Keeping this shape here means release persistence does not depend
// on Access's PostgreSQL storage package (or any other sibling authority).
// The repository only needs the error boundary today; the complete projection
// lets adapters validate replay identity before returning.
type AuditEvent struct {
	AuditID           string
	DomainEventID     string
	ScopeID           string
	ActorID           string
	PrincipalID       string
	Source            string
	Operation         string
	Action            string
	ResourceKind      string
	ResourceID        string
	Capability        access.Capability
	Outcome           string
	RequestID         string
	RequestDigest     string
	CorrelationID     string
	AggregateKey      string
	AggregateSequence int64
	MetadataJSON      string
	OccurredAt        time.Time
	IntentDigest      string
}

// EventInput describes the durable release event emitted after a state
// transition. It deliberately mirrors only the event-log contract needed by
// release; the platform event repository remains an app-composition detail.
type EventInput struct {
	EventID       string
	ScopeID       string
	AggregateType string
	AggregateID   string
	EventType     string
	SchemaVersion int64
	CorrelationID string
	Payload       json.RawMessage
}

// Event is the release-owned durable event projection. Adapters map this to
// the platform event authority and validate all immutable replay fields.
type Event struct {
	EventID          string
	ScopeID          string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	EventType        string
	SchemaVersion    int64
	OccurredAt       time.Time
	CorrelationID    string
	Payload          json.RawMessage
}

// Options wires transactional side effects. Event and audit appenders are
// optional for isolated persistence tests; when an intent is supplied without
// an audit appender the mutation fails closed.
type Options struct {
	Audit    AuditAppender
	Events   EventAppender
	Workflow WorkflowAppender
}

type Repository struct {
	db       DBTX
	audit    AuditAppender
	events   EventAppender
	workflow WorkflowAppender
}

var (
	ErrInvalid  = release.ErrInvalid
	ErrNotFound = release.ErrNotFound
	ErrConflict = release.ErrConflict
)

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception: schema-ddl. Capability-owned schema, guards and triggers
	// are applied by the baseline through a caller-owned migration transaction.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository           { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository { return New(db) }
func NewWithOptions(db DBTX, options Options) *Repository {
	return &Repository{db: db, audit: options.Audit, events: options.Events, workflow: options.Workflow}
}

// PostgreSQLAuthority marks this repository as the native release capability.
// Module composition uses the marker to reject SQLite or test doubles in a
// production process unless they explicitly implement the same authority
// contract.
func (*Repository) PostgreSQLAuthority() {}

// Configured reports whether the repository has a usable native handle. It is
// intentionally a shallow check; pool readiness and schema revision remain
// application lifecycle concerns.
func (r *Repository) Configured() bool { return r != nil && r.db != nil }

// AuditCapable reports whether transactional release mutations can append
// their canonical audit intent. Production composition requires this
// capability; isolated repository tests may intentionally omit it.
func (r *Repository) AuditCapable() bool { return r != nil && r.audit != nil }

// EventCapable reports whether release transitions can append durable domain
// events. Production composition requires this capability; read-only and
// focused persistence tests may omit it.
func (r *Repository) EventCapable() bool { return r != nil && r.events != nil }

// WorkflowCapable reports whether finalization can persist a durable workflow
// intent in the release transaction.
func (r *Repository) WorkflowCapable() bool { return r != nil && r.workflow != nil }

func (r *Repository) WithTx(tx Tx) *Repository {
	if r == nil {
		return &Repository{}
	}
	return &Repository{db: tx, audit: r.audit, events: r.events, workflow: r.workflow}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (r *Repository) Create(ctx context.Context, input release.CreateInput) (release.Release, error) {
	if r == nil || r.db == nil {
		return release.Release{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return release.Release{}, errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	row, err := r.CreateTx(ctx, tx, input)
	if err != nil {
		return release.Release{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return release.Release{}, err
	}
	return row, nil
}

func (r *Repository) CreateTx(ctx context.Context, tx Tx, input release.CreateInput) (release.Release, error) {
	if tx == nil {
		return release.Release{}, ErrInvalid
	}
	identity, err := validateCreate(input)
	if err != nil {
		return release.Release{}, err
	}
	connections := normalizeConnections(input.Connections)
	encoded, err := json.Marshal(input.Provenance)
	if err != nil || len(encoded) > 65536 {
		return release.Release{}, ErrInvalid
	}
	q := releasedb.New(tx)
	inserted, err := q.InsertRelease(contextOrBackground(ctx), releasedb.InsertReleaseParams{
		ReleaseID: input.ID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		GenerationID: identity.GenerationID, ProjectDigest: input.ProjectDigest, ArtifactDigest: input.ArtifactDigest,
		RequestDigest: input.RequestDigest, IdempotencyKey: input.IdempotencyKey, Provenance: encoded, CreatedBy: input.CreatedBy,
	})
	if err != nil {
		return release.Release{}, mapError(err)
	}
	row, err := q.GetReleaseByIdempotency(contextOrBackground(ctx), releasedb.GetReleaseByIdempotencyParams{ProjectID: identity.ProjectID.String(), IdempotencyKey: input.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		if inserted == 0 {
			return release.Release{}, ErrConflict
		}
		return release.Release{}, ErrNotFound
	}
	if err != nil {
		return release.Release{}, err
	}
	stored, err := r.getTx(ctx, tx, identity.ProjectID, row.ReleaseID)
	if err != nil {
		return release.Release{}, err
	}
	if stored.ID != input.ID || stored.ServingIdentity != identity || stored.RequestDigest != input.RequestDigest || stored.ProjectDigest != input.ProjectDigest || stored.ArtifactDigest != input.ArtifactDigest || stored.CreatedBy != input.CreatedBy || stored.Provenance == nil || stored.Provenance.Digest != input.Provenance.Digest {
		return release.Release{}, fmt.Errorf("%w: release idempotency identity differs", ErrConflict)
	}
	// The sqlc affected-row count is the authoritative new-row signal. Exact
	// replays validate immutable pins and do not append side effects again.
	if inserted == 1 {
		for _, pin := range connections {
			if err := q.InsertReleaseConnection(contextOrBackground(ctx), releasedb.InsertReleaseConnectionParams{ReleaseID: input.ID, ConnectionID: pin.ConnectionID, RevisionID: pin.RevisionID}); err != nil {
				return release.Release{}, mapError(err)
			}
		}
		if err := r.recordAuditAndEvent(ctx, tx, stored, "release.created"); err != nil {
			return release.Release{}, err
		}
	} else {
		// Validate that all immutable connection pins are identical on replay.
		if err := r.compareConnections(ctx, tx, input.ID, connections); err != nil {
			return release.Release{}, err
		}
	}
	return r.getTx(ctx, tx, identity.ProjectID, input.ID)
}

func validateCreate(input release.CreateInput) (projectgraph.ServingIdentity, error) {
	identity, err := input.Identity()
	if err != nil || input.ID == "" || input.ID != strings.TrimSpace(input.ID) || input.CreatedBy == "" || input.CreatedBy != strings.TrimSpace(input.CreatedBy) || input.IdempotencyKey == "" || input.IdempotencyKey != strings.TrimSpace(input.IdempotencyKey) {
		return projectgraph.ServingIdentity{}, ErrInvalid
	}
	if !validDigest(input.ProjectDigest) || !validDigest(input.ArtifactDigest) || !validDigest(input.RequestDigest) || input.Provenance == nil || input.Provenance.Validate() != nil {
		return projectgraph.ServingIdentity{}, ErrInvalid
	}
	if input.Provenance.Plan.Identity != identity || input.Provenance.Artifact.ProjectDigest != input.ProjectDigest || input.Provenance.Artifact.ContentDigest != input.ArtifactDigest {
		return projectgraph.ServingIdentity{}, ErrInvalid
	}
	seen := map[string]struct{}{}
	for _, pin := range input.Connections {
		if pin.ConnectionID == "" || pin.ConnectionID != strings.TrimSpace(pin.ConnectionID) || pin.RevisionID == "" || pin.RevisionID != strings.TrimSpace(pin.RevisionID) {
			return projectgraph.ServingIdentity{}, ErrInvalid
		}
		if _, ok := seen[pin.ConnectionID]; ok {
			return projectgraph.ServingIdentity{}, ErrInvalid
		}
		seen[pin.ConnectionID] = struct{}{}
	}
	return identity, nil
}

func validDigest(v string) bool {
	return len(v) == 71 && strings.HasPrefix(v, "sha256:") && len(strings.Trim(v[7:], "0123456789abcdef")) == 0
}

func (r *Repository) Get(ctx context.Context, projectID projectgraph.ResourceID, releaseID string) (release.Release, error) {
	if r == nil || r.db == nil || projectID.Validate() != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return release.Release{}, ErrInvalid
	}
	return r.getTx(ctx, r.db, projectID, releaseID)
}

func (r *Repository) getTx(ctx context.Context, db DBTX, projectID projectgraph.ResourceID, releaseID string) (release.Release, error) {
	row, err := releasedb.New(db).GetRelease(contextOrBackground(ctx), releasedb.GetReleaseParams{ProjectID: projectID.String(), ReleaseID: releaseID})
	if errors.Is(err, pgx.ErrNoRows) {
		return release.Release{}, ErrNotFound
	}
	if err != nil {
		return release.Release{}, err
	}
	return r.loadRow(ctx, db, row)
}

func (r *Repository) loadRow(ctx context.Context, db DBTX, row releasedb.GetReleaseRow) (release.Release, error) {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
	if err != nil {
		return release.Release{}, fmt.Errorf("release has invalid serving identity: %w", err)
	}
	out := release.Release{ID: row.ReleaseID, ServingIdentity: identity, ProjectDigest: row.ProjectDigest, ArtifactDigest: row.ArtifactDigest, ActualDigest: row.ArtifactActualDigest, ArtifactSizeBytes: row.ArtifactSizeBytes, RequestDigest: row.RequestDigest, IdempotencyKey: row.IdempotencyKey, Status: release.Status(row.Status), CreatedBy: row.CreatedBy, Error: row.Error}
	out.CreatedAt = formatTimestamp(row.CreatedAt)
	out.ArtifactUploadedAt = formatOptionalTimestamp(row.ArtifactUploadedAt)
	out.FinalizedAt = formatOptionalTimestamp(row.FinalizedAt)
	if row.Provenance != "" && row.Provenance != "{}" {
		var p release.Provenance
		if err := json.Unmarshal([]byte(row.Provenance), &p); err != nil || p.Validate() != nil {
			return release.Release{}, release.ErrProvenanceInvalid
		}
		out.Provenance = &p
	}
	pins, err := releasedb.New(db).ListReleaseConnections(contextOrBackground(ctx), row.ReleaseID)
	if err != nil {
		return release.Release{}, err
	}
	for _, pin := range pins {
		out.Manifest.Connections = append(out.Manifest.Connections, release.ConnectionPin{ConnectionID: pin.ConnectionID, RevisionID: pin.RevisionID})
	}
	return out, nil
}

func formatTimestamp(v pgtype.Timestamptz) string {
	if !v.Valid {
		return ""
	}
	return v.Time.UTC().Format(time.RFC3339Nano)
}
func formatOptionalTimestamp(v pgtype.Timestamptz) string { return formatTimestamp(v) }

func (r *Repository) List(ctx context.Context, projectID projectgraph.ResourceID) ([]release.Release, error) {
	if r == nil || r.db == nil || projectID.Validate() != nil {
		return nil, ErrInvalid
	}
	rows, err := releasedb.New(r.db).ListReleases(contextOrBackground(ctx), projectID.String())
	if err != nil {
		return nil, err
	}
	out := make([]release.Release, 0, len(rows))
	for _, row := range rows {
		item, err := r.getTx(ctx, r.db, projectID, row.ReleaseID)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (r *Repository) compareConnections(ctx context.Context, db DBTX, releaseID string, want []release.ConnectionPin) error {
	want = normalizeConnections(want)
	rows, err := releasedb.New(db).ListReleaseConnections(contextOrBackground(ctx), releaseID)
	if err != nil {
		return err
	}
	if len(rows) != len(want) {
		return ErrConflict
	}
	for i, pin := range want {
		if rows[i].ConnectionID != pin.ConnectionID || rows[i].RevisionID != pin.RevisionID {
			return ErrConflict
		}
	}
	return nil
}

// normalizeConnections gives the repository its own canonical pin order. The
// service currently sorts candidate pins, but callers may invoke this native
// authority directly; exact idempotent replays must not depend on input order.
func normalizeConnections(pins []release.ConnectionPin) []release.ConnectionPin {
	if len(pins) == 0 {
		return nil
	}
	out := append([]release.ConnectionPin(nil), pins...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ConnectionID == out[j].ConnectionID {
			return out[i].RevisionID < out[j].RevisionID
		}
		return out[i].ConnectionID < out[j].ConnectionID
	})
	return out
}

func (r *Repository) RecordArtifact(ctx context.Context, artifact release.Artifact) error {
	if r == nil || r.db == nil {
		return ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	if err := r.RecordArtifactTx(ctx, tx, artifact); err != nil {
		return err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return err
	}
	return nil
}

func (r *Repository) RecordArtifactTx(ctx context.Context, tx Tx, artifact release.Artifact) error {
	if tx == nil || artifact.ReleaseID == "" || artifact.SizeBytes < 0 || artifact.ServingIdentity.Validate() != nil || !validDigest(artifact.ExpectedDigest) || artifact.ActualDigest != artifact.ExpectedDigest {
		return ErrInvalid
	}
	actual := artifact.ActualDigest
	tag, err := releasedb.New(tx).RecordArtifact(contextOrBackground(ctx), releasedb.RecordArtifactParams{ArtifactActualDigest: &actual, ArtifactSizeBytes: artifact.SizeBytes, ReleaseID: artifact.ReleaseID, ProjectID: artifact.ServingIdentity.ProjectID.String(), Environment: artifact.ServingIdentity.Environment, GenerationID: artifact.ServingIdentity.GenerationID, ArtifactDigest: artifact.ExpectedDigest})
	if err != nil {
		return err
	}
	if tag != 1 {
		current, loadErr := r.getTx(ctx, tx, artifact.ServingIdentity.ProjectID, artifact.ReleaseID)
		if loadErr != nil {
			return loadErr
		}
		if current.Status == release.StatusDraft && current.ArtifactUploadedAt != "" && current.ActualDigest == artifact.ActualDigest && current.ArtifactSizeBytes == artifact.SizeBytes {
			return nil // exact replay of the immutable artifact evidence
		}
		return ErrConflict
	}
	row, err := r.getTx(ctx, tx, artifact.ServingIdentity.ProjectID, artifact.ReleaseID)
	if err != nil {
		return err
	}
	return r.recordAuditAndEvent(ctx, tx, row, "release.artifact_uploaded")
}

func (r *Repository) BeginFinalization(ctx context.Context, projectID, releaseID string, workflow publicjobs.WorkflowIntent) (release.Release, error) {
	if r == nil || r.db == nil {
		return release.Release{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return release.Release{}, errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	row, err := r.BeginFinalizationTx(ctx, tx, projectID, releaseID, workflow)
	if err != nil {
		return release.Release{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return release.Release{}, err
	}
	return row, nil
}

func (r *Repository) BeginFinalizationTx(ctx context.Context, tx Tx, projectID, releaseID string, workflow publicjobs.WorkflowIntent) (release.Release, error) {
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return release.Release{}, ErrInvalid
	}
	_, err = releasedb.New(tx).GetReleaseForUpdate(contextOrBackground(ctx), releasedb.GetReleaseForUpdateParams{ProjectID: projectID, ReleaseID: releaseID})
	if errors.Is(err, pgx.ErrNoRows) {
		return release.Release{}, ErrNotFound
	}
	if err != nil {
		return release.Release{}, err
	}
	current, err := r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if current.Status != release.StatusDraft && current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	if (workflow.Event.Key != "" || workflow.Job.ID != "") && r.workflow == nil {
		return release.Release{}, errors.New("release workflow appender is required")
	}
	if workflow.Event.Key != "" || workflow.Job.ID != "" {
		if err := r.workflow.RecordWorkflow(contextOrBackground(ctx), tx, workflow); err != nil {
			return release.Release{}, err
		}
	}
	transitioned := false
	if current.Status == release.StatusDraft {
		if current.ArtifactUploadedAt == "" || current.ActualDigest != current.ArtifactDigest {
			return release.Release{}, release.ErrIncomplete
		}
		tag, updateErr := releasedb.New(tx).MarkValidating(contextOrBackground(ctx), releasedb.MarkValidatingParams{ReleaseID: releaseID, ProjectID: projectID})
		if updateErr != nil {
			return release.Release{}, updateErr
		}
		if tag != 1 {
			return release.Release{}, ErrConflict
		}
		transitioned = true
	}
	_, err = releasedb.New(tx).GetRelease(contextOrBackground(ctx), releasedb.GetReleaseParams{ProjectID: projectID, ReleaseID: releaseID})
	if err != nil {
		return release.Release{}, err
	}
	current, err = r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if transitioned {
		if err := r.recordAuditAndEvent(ctx, tx, current, "release.validating"); err != nil {
			return release.Release{}, err
		}
	}
	_ = id
	return current, nil
}

func (r *Repository) CompleteFinalization(ctx context.Context, projectID, releaseID, actualDigest string) (release.Release, error) {
	if r == nil || r.db == nil || !validDigest(actualDigest) {
		return release.Release{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return release.Release{}, errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	row, err := r.CompleteFinalizationTx(ctx, tx, projectID, releaseID, actualDigest)
	if err != nil {
		return release.Release{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return release.Release{}, err
	}
	return row, nil
}

func (r *Repository) CompleteFinalizationTx(ctx context.Context, tx Tx, projectID, releaseID, actualDigest string) (release.Release, error) {
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil || !validDigest(actualDigest) {
		return release.Release{}, ErrInvalid
	}
	current, err := r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if current.Status == release.StatusReady {
		if current.ActualDigest != actualDigest {
			return release.Release{}, ErrConflict
		}
		return current, nil
	}
	if current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	if actualDigest != current.ArtifactDigest || current.ArtifactUploadedAt == "" {
		return release.Release{}, ErrConflict
	}
	tag, err := releasedb.New(tx).MarkReady(contextOrBackground(ctx), releasedb.MarkReadyParams{ReleaseID: releaseID, ProjectID: projectID})
	if err != nil {
		return release.Release{}, err
	}
	if tag != 1 {
		// Another transaction may have won the validating -> ready transition
		// while this caller waited on the row lock. Exact completion is a safe
		// idempotent replay; divergent or otherwise terminal state preserves the
		// existing conflict/immutable semantics.
		latest, reloadErr := r.getTx(ctx, tx, id, releaseID)
		if reloadErr != nil {
			return release.Release{}, reloadErr
		}
		if latest.Status == release.StatusReady {
			if latest.ActualDigest == actualDigest {
				return latest, nil
			}
			return release.Release{}, ErrConflict
		}
		if latest.Status != release.StatusValidating {
			return release.Release{}, release.ErrImmutable
		}
		return release.Release{}, ErrConflict
	}
	ready, err := r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if err := r.recordAuditAndEvent(ctx, tx, ready, "release.ready"); err != nil {
		return release.Release{}, err
	}
	return ready, nil
}

// CompleteFinalizationJobTx is the River worker boundary. Typed River args
// carry only the stable product identity and canonical request digest; the
// worker reloads the authoritative artifact digest inside the same
// transaction that records both the product terminal state and River job
// completion.
func (r *Repository) CompleteFinalizationJobTx(ctx context.Context, tx Tx, projectID, releaseID, requestDigest string) (release.Release, error) {
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil || releaseID == "" || !validDigest(requestDigest) {
		return release.Release{}, ErrInvalid
	}
	current, err := r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if current.RequestDigest != requestDigest {
		return release.Release{}, ErrConflict
	}
	return r.CompleteFinalizationTx(ctx, tx, projectID, releaseID, current.ArtifactDigest)
}

func (r *Repository) FailFinalization(ctx context.Context, projectID, releaseID string, cause error) (release.Release, error) {
	if r == nil || r.db == nil {
		return release.Release{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return release.Release{}, errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	row, err := r.FailFinalizationTx(ctx, tx, projectID, releaseID, cause)
	if err != nil {
		return release.Release{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return release.Release{}, err
	}
	return row, nil
}

func (r *Repository) FailFinalizationTx(ctx context.Context, tx Tx, projectID, releaseID string, cause error) (release.Release, error) {
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return release.Release{}, ErrInvalid
	}
	current, err := r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if current.Status == release.StatusFailed {
		return current, nil
	}
	if current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	message := "release validation failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = cause.Error()
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	tag, err := releasedb.New(tx).MarkFailed(contextOrBackground(ctx), releasedb.MarkFailedParams{Error: message, ReleaseID: releaseID, ProjectID: projectID})
	if err != nil {
		return release.Release{}, err
	}
	if tag != 1 {
		return release.Release{}, ErrConflict
	}
	failed, err := r.getTx(ctx, tx, id, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if err := r.recordAuditAndEvent(ctx, tx, failed, "release.failed"); err != nil {
		return release.Release{}, err
	}
	return failed, nil
}

func (r *Repository) recordAuditAndEvent(ctx context.Context, tx Tx, row release.Release, eventType string) error {
	intent, hasIntent := release.AuditIntentFromContext(ctx)
	if hasIntent {
		if r.audit == nil {
			return errors.New("release audit appender is required")
		}
		if _, err := r.audit.RecordAuditEvent(contextOrBackground(ctx), tx, intent); err != nil {
			return err
		}
	}
	if r.events == nil {
		return nil
	}
	payload, err := json.Marshal(release.FinalizationEventData(row))
	if err != nil {
		return err
	}
	event, err := r.events.AppendEvent(contextOrBackground(ctx), tx, EventInput{ScopeID: row.ServingIdentity.ProjectID.String(), AggregateType: "release", AggregateID: row.ID, EventType: eventType, SchemaVersion: 1, Payload: payload})
	if err != nil {
		return err
	}
	if event.ScopeID != row.ServingIdentity.ProjectID.String() || event.AggregateType != "release" || event.AggregateID != row.ID || event.EventType != eventType || event.SchemaVersion != 1 || !jsonEqual(event.Payload, payload) {
		return fmt.Errorf("%w: release event identity differs", ErrConflict)
	}
	return err
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	la, err := json.Marshal(a)
	if err != nil {
		return false
	}
	ra, err := json.Marshal(b)
	return err == nil && string(la) == string(ra)
}

func (r *Repository) RetainCandidateProvenance(ctx context.Context, projectID projectgraph.ResourceID, p release.Provenance) (release.Provenance, error) {
	if r == nil || r.db == nil || projectID.Validate() != nil || p.Validate() != nil || p.Plan.Identity.ProjectID != projectID {
		return release.Provenance{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return release.Provenance{}, errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return release.Provenance{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	out, err := r.retainCandidateProvenanceTx(ctx, tx, projectID, p)
	if err != nil {
		return release.Provenance{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return release.Provenance{}, err
	}
	return out, nil
}

func (r *Repository) RetainCandidateProvenanceTx(ctx context.Context, tx Tx, projectID projectgraph.ResourceID, p release.Provenance) (release.Provenance, error) {
	if tx == nil {
		return release.Provenance{}, ErrInvalid
	}
	return r.retainCandidateProvenanceTx(ctx, tx, projectID, p)
}
func (r *Repository) retainCandidateProvenanceTx(ctx context.Context, tx Tx, projectID projectgraph.ResourceID, p release.Provenance) (release.Provenance, error) {
	if projectID.Validate() != nil || p.Validate() != nil || p.Plan.Identity.ProjectID != projectID {
		return release.Provenance{}, ErrInvalid
	}
	encoded, err := json.Marshal(p)
	if err != nil || len(encoded) > 65536 {
		return release.Provenance{}, ErrInvalid
	}
	if err := releasedb.New(tx).InsertCandidateProvenance(contextOrBackground(ctx), releasedb.InsertCandidateProvenanceParams{ProjectID: projectID.String(), CandidateID: p.Candidate.ID, CandidateRevision: p.Candidate.Revision, ProvenanceDigest: p.Digest, Provenance: encoded}); err != nil {
		return release.Provenance{}, err
	}
	stored, err := r.CandidateProvenanceTx(ctx, tx, projectID, p.Candidate.ID, p.Candidate.Revision)
	if err != nil {
		return release.Provenance{}, err
	}
	if stored.Digest != p.Digest {
		return release.Provenance{}, ErrConflict
	}
	return stored, nil
}

func (r *Repository) CandidateProvenance(ctx context.Context, projectID projectgraph.ResourceID, candidateID string, revision int64) (release.Provenance, error) {
	if r == nil || r.db == nil {
		return release.Provenance{}, ErrInvalid
	}
	return r.CandidateProvenanceTx(ctx, r.db, projectID, candidateID, revision)
}
func (r *Repository) CandidateProvenanceTx(ctx context.Context, db DBTX, projectID projectgraph.ResourceID, candidateID string, revision int64) (release.Provenance, error) {
	if projectID.Validate() != nil || candidateID == "" || candidateID != strings.TrimSpace(candidateID) || revision < 1 {
		return release.Provenance{}, ErrInvalid
	}
	row, err := releasedb.New(db).GetCandidateProvenance(contextOrBackground(ctx), releasedb.GetCandidateProvenanceParams{ProjectID: projectID.String(), CandidateID: candidateID, CandidateRevision: revision})
	if errors.Is(err, pgx.ErrNoRows) {
		return release.Provenance{}, ErrNotFound
	}
	if err != nil {
		return release.Provenance{}, err
	}
	var p release.Provenance
	if json.Unmarshal([]byte(row.Provenance), &p) != nil || p.Validate() != nil || p.Digest != row.ProvenanceDigest {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	return p, nil
}

func (r *Repository) ProvenanceForServingState(ctx context.Context, identity projectgraph.ServingIdentity) (release.Provenance, error) {
	if r == nil || r.db == nil || identity.Validate() != nil {
		return release.Provenance{}, ErrInvalid
	}
	raw, err := releasedb.New(r.db).GetReadyReleaseProvenanceByGeneration(contextOrBackground(ctx), releasedb.GetReadyReleaseProvenanceByGenerationParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID})
	if errors.Is(err, pgx.ErrNoRows) {
		rows, candidateErr := releasedb.New(r.db).ListCandidateProvenanceByGeneration(contextOrBackground(ctx), releasedb.ListCandidateProvenanceByGenerationParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID})
		if candidateErr != nil {
			return release.Provenance{}, candidateErr
		}
		if len(rows) == 0 {
			return release.Provenance{}, ErrNotFound
		}
		if len(rows) != 1 {
			return release.Provenance{}, ErrConflict
		}
		raw = rows[0]
	} else if err != nil {
		return release.Provenance{}, err
	}
	var p release.Provenance
	if json.Unmarshal([]byte(raw), &p) != nil || p.Validate() != nil || p.Plan.Identity != identity {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	return p, nil
}

func (r *Repository) LinkDeployment(ctx context.Context, projectID, deploymentID, releaseID, rollbackOf string) error {
	if r == nil || r.db == nil {
		return ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("release PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	if err := r.LinkDeploymentTx(ctx, tx, projectID, deploymentID, releaseID, rollbackOf); err != nil {
		return err
	}
	return tx.Commit(contextOrBackground(ctx))
}
func (r *Repository) LinkDeploymentTx(ctx context.Context, tx Tx, projectID, deploymentID, releaseID, rollbackOf string) error {
	if tx == nil || !canonicalID(projectID) || !canonicalID(deploymentID) || !canonicalID(releaseID) || rollbackOf != strings.TrimSpace(rollbackOf) {
		return ErrInvalid
	}
	if rollbackOf == releaseID {
		return ErrInvalid
	}
	row, err := r.getTx(ctx, tx, projectgraph.ResourceID(projectID), releaseID)
	if err != nil {
		return err
	}
	if row.ServingIdentity.ProjectID.String() != projectID {
		return ErrConflict
	}
	if rollbackOf != "" {
		prior, priorErr := r.getTx(ctx, tx, projectgraph.ResourceID(projectID), rollbackOf)
		if priorErr != nil || prior.ServingIdentity.ProjectID.String() != projectID {
			return ErrConflict
		}
	}
	if err := releasedb.New(tx).InsertDeploymentLinkage(contextOrBackground(ctx), releasedb.InsertDeploymentLinkageParams{DeploymentID: deploymentID, ProjectID: projectID, ReleaseID: releaseID, RollbackOf: rollbackOf}); err != nil {
		return mapError(err)
	}
	stored, err := releasedb.New(tx).GetDeploymentLinkageByID(contextOrBackground(ctx), deploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if stored.ProjectID != projectID || stored.ReleaseID != releaseID || stored.RollbackOf != rollbackOf {
		return ErrConflict
	}
	return nil
}
func canonicalID(v string) bool { return v != "" && v == strings.TrimSpace(v) && len(v) <= 255 }
func (r *Repository) DeploymentRelease(ctx context.Context, projectID, deploymentID string) (string, string, error) {
	if r == nil || r.db == nil || !canonicalID(projectID) || !canonicalID(deploymentID) {
		return "", "", ErrInvalid
	}
	row, err := releasedb.New(r.db).GetDeploymentLinkage(contextOrBackground(ctx), releasedb.GetDeploymentLinkageParams{ProjectID: projectID, DeploymentID: deploymentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return row.ReleaseID, row.RollbackOf, err
}
func (r *Repository) ListDeploymentIDs(ctx context.Context, projectID string) ([]string, error) {
	if r == nil || r.db == nil || !canonicalID(projectID) {
		return nil, ErrInvalid
	}
	return releasedb.New(r.db).ListDeploymentIDs(contextOrBackground(ctx), projectID)
}
func (r *Repository) PriorDeploymentRelease(ctx context.Context, projectID, deploymentID string) (string, error) {
	if r == nil || r.db == nil || !canonicalID(projectID) || !canonicalID(deploymentID) {
		return "", ErrInvalid
	}
	row, err := releasedb.New(r.db).GetPriorDeploymentRelease(contextOrBackground(ctx), releasedb.GetPriorDeploymentReleaseParams{ProjectID: projectID, DeploymentID: deploymentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return row, err
}

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate key") || strings.Contains(strings.ToLower(err.Error()), "unique constraint") || strings.Contains(strings.ToLower(err.Error()), "constraint") {
		return ErrConflict
	}
	return err
}

var _ release.Repository = (*Repository)(nil)
var _ release.FinalizationUnitOfWork = (*Repository)(nil)
var _ release.CandidateProvenanceRepository = (*Repository)(nil)
var _ release.ServingStateProvenanceRepository = (*Repository)(nil)
