// Package postgres implements the canonical PostgreSQL delivery authority.
//
// The repository is deliberately independent from the legacy SQLite
// deployment store and from the DuckLake catalog repository.  PostgreSQL
// records identity, qualification and activation evidence; the catalog and
// object store remain separate physical authorities.
package postgres

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/deployment"
	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is implemented by pgx connections, transactions and pools.  A caller
// may pass a transaction to the Tx methods to compose activation with another
// control mutation without crossing a database boundary.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is the caller-owned native transaction surface. Pools and connections
// intentionally do not satisfy it, preventing accidental split-brain writes.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// MaintenanceDBTX is the separately authenticated control-plane retention
// surface. PostgreSQL function grants, rather than Go type shape, enforce the
// destructive boundary.
type MaintenanceDBTX interface{ DBTX }

// Maintenance owns bounded retention-root drain work. Request-serving code
// receives Repository instead and therefore has no batch-expiry method.
type Maintenance struct{ db MaintenanceDBTX }

// RetentionDrainResult is the exact work committed by one bounded pass.
type RetentionDrainResult struct {
	Retired int64
	Expired int64
}

var (
	ErrInvalid       = errors.New("invalid delivery authority input")
	ErrConflict      = errors.New("delivery authority identity conflict")
	ErrNotFound      = errors.New("delivery authority record not found")
	ErrNotQualified  = errors.New("delivery candidate is not fully qualified")
	ErrStaleFence    = errors.New("delivery lease fencing epoch is stale")
	ErrLeaseExpired  = errors.New("delivery lease is expired")
	ErrLeaseBusy     = errors.New("delivery target lease is owned by another worker")
	ErrCASConflict   = errors.New("delivery target compare-and-swap conflict")
	ErrAlreadyActive = errors.New("delivery publication is already active")
)

const (
	maxText     = 255
	maxEvidence = 32768
	// maxPlanDocument bounds the complete canonical deployment.DeliveryPlan
	// persisted for native build rehydration. It is intentionally independent
	// from the smaller redacted evidence projection limit.
	maxPlanDocument = 1 << 20
	maxLease        = 24 * time.Hour
)

// DeliveryTarget is the single mutable project/environment target fence.
type DeliveryTarget struct {
	TargetID            string
	ProjectID           string
	Environment         string
	TargetRevision      int64
	ActiveGenerationID  string
	ActivePublicationID string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// DeliveryOperatorSnapshot is the bounded native operator projection. The
// PostgreSQL delivery authority persists target identity and the active
// generation/publication pointers; SQLite-only lease/retention projections
// are deliberately not synthesized here.
type DeliveryOperatorSnapshot struct {
	ProjectID           string
	Environment         string
	TargetID            string
	TargetRevision      int64
	ActiveGenerationID  string
	ActivePublicationID string
}

type TargetInput struct {
	TargetID, ProjectID, Environment string
	TargetRevision                   int64
}

type DeliveryPlan struct {
	PlanID, TargetID, PlanDigest                                         string
	PlanRevision                                                         int64
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint string
	ArtifactDigest, QualificationDigest                                  string
	QualificationRequired                                                bool
	ApprovalRequired                                                     bool
	ApprovalPolicyRevision                                               int64
	// PlanDocument is the complete canonical JSON serialization of the rich
	// deployment.DeliveryPlan. PostgreSQL stores this document as jsonb; the
	// repository canonicalizes it again on reads so native builders always
	// receive the exact execution contract rather than a projection.
	PlanDocument json.RawMessage
	Evidence     json.RawMessage
	CreatedAt    time.Time
}
type PlanInput = DeliveryPlan

type BuildAttemptState string

const (
	AttemptRunning       BuildAttemptState = "running"
	AttemptCommitted     BuildAttemptState = "committed"
	AttemptAborted       BuildAttemptState = "aborted"
	AttemptIndeterminate BuildAttemptState = "indeterminate"
	AttemptFenced        BuildAttemptState = "fenced"
)

type DeliveryBuildAttempt struct {
	AttemptID, PlanID, CandidateID    string
	OwnerID                           string
	PhysicalPoolID, CatalogID         string
	FencingEpoch                      int64
	RequestDigest, PlanDigest         string
	State                             BuildAttemptState
	Namespace, SessionIdentity        string
	LeaseExpiresAt                    time.Time
	SnapshotID                        int64
	CommitMarker, TerminationEvidence json.RawMessage
	CreatedAt, UpdatedAt, FinishedAt  time.Time
}

type BuildAttemptInput struct {
	AttemptID, PlanID, CandidateID string
	OwnerID                        string
	PhysicalPoolID, CatalogID      string
	FencingEpoch                   int64
	RequestDigest, PlanDigest      string
	Namespace, SessionIdentity     string
	LeaseExpiresAt                 time.Time
}

// BuildAttemptSuccessorInput is the caller-owned transaction input for
// recovery after an exact physical marker lookup returned "absent" without
// proving that the predecessor session terminated. The predecessor must have
// already been marked indeterminate by the owner/reconciler; the resolution
// document is separate evidence for the immutable successor edge. A lease
// timeout alone must never authorize a successor.
//
// SuccessorAttempt.Namespace and FencingEpoch are authority-derived and must
// be left empty/zero.  The repository derives a fresh relation namespace from
// the successor candidate, attempt UUID, and newly allocated target fence.
type BuildAttemptSuccessorInput struct {
	Predecessor          LeaseFence
	PredecessorAttemptID string
	CatalogID            string
	ResolutionEvidence   json.RawMessage
	SuccessorLease       LeaseInput
	SuccessorAttempt     BuildAttemptInput
}

// BuildAttemptSuccessorResult contains the immutable predecessor->successor
// edge and the fresh target lease/attempt admitted by one control transaction.
type BuildAttemptSuccessorResult struct {
	Predecessor        DeliveryBuildAttempt
	SuccessorLease     DeliveryLease
	Successor          DeliveryBuildAttempt
	ResolutionEvidence json.RawMessage
}

// BuildAttemptSuccessorLink is the durable immutable predecessor edge.  It is
// read-only evidence used to fence late predecessor commit/reconcile calls.
type BuildAttemptSuccessorLink struct {
	PredecessorAttemptID string
	SuccessorAttemptID   string
	ResolutionEvidence   json.RawMessage
	CreatedAt            time.Time
}

// BuildArtifactBinding is the immutable artifact hand-off for one build
// attempt. An attempt can have at most one binding; retries may only replay
// the exact artifact and serving-state identity.
type BuildArtifactBinding struct {
	AttemptID             string
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
	BoundAt               time.Time
}

type BuildArtifactBindingInput struct {
	AttemptID             string
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
	OwnerID               string
	FencingEpoch          int64
}

// RecoveredBuildArtifactBindingInput is the exact physical-build evidence
// needed to bind an artifact while recovering an attempt. Unlike the normal
// binding input, recovery also carries the canonical DuckLake commit marker;
// this prevents a recovered artifact from being attached to a different
// attempt, plan, request, pool, or serving generation.
type RecoveredBuildArtifactBindingInput struct {
	AttemptID             string
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
	OwnerID               string
	FencingEpoch          int64
	CommitMarker          json.RawMessage
}

type CommitAttemptInput struct {
	AttemptID, OwnerID string
	FencingEpoch       int64
	SnapshotID         int64
	CommitMarker       json.RawMessage
}
type TerminateAttemptInput struct {
	AttemptID, OwnerID string
	FencingEpoch       int64
	Evidence           json.RawMessage
}

// ReconcileBuildAttemptInput is exact restart evidence for one previously
// running (or indeterminate) build attempt. Unlike CommitAttemptInput, this
// explicit recovery input is allowed to complete an expired attempt lease;
// the supplied marker or positive session-termination evidence is the guard.
// State must be committed or aborted.
type ReconcileBuildAttemptInput struct {
	AttemptID, OwnerID  string
	FencingEpoch        int64
	SnapshotID          int64
	CommitMarker        json.RawMessage
	TerminationEvidence json.RawMessage
	SessionTerminated   bool
	SessionIdentity     string
	State               BuildAttemptState
}

// SnapshotSeal is immutable qualification evidence.  Every field that can
// affect execution or routing is relational, never hidden in evidence JSON.
type SnapshotSeal struct {
	SealID, AttemptID, CandidateID                                                                                   string
	PhysicalPoolID, TenantDomain, Region, EncryptionDomain, ObjectNamespace, CatalogDatabase, CatalogID, CatalogUUID string
	CatalogVersion, DuckLakeSnapshotID                                                                               int64
	RelationNamespace, ObjectRoot, ObjectRootDigest, ArtifactRoot, ArtifactRootDigest                                string
	RelationManifestDigest, ClosureDigest                                                                            string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint                                             string
	RequestDigest, PlanDigest, CompatibilityDigest, ServingArtifactID, ServingArtifactDigest                         string
	DuckDBVersion, RuntimeVersion, DuckLakeExtensionVersion, DuckLakeSpecVersion, CatalogSchemaVersion               string
	QualificationEvidence                                                                                            json.RawMessage
	QualifiedAt                                                                                                      time.Time
}
type SnapshotSealInput = SnapshotSeal

type DeliveryCandidate struct {
	CandidateID, TargetID, PlanID, AttemptID, SnapshotSealID string
	Status                                                   string
	CandidateRevision                                        int64
	ArtifactDigest, QualificationDigest                      string
	CreatedAt, QualifiedAt, RetiredAt                        time.Time
}
type CandidateInput = DeliveryCandidate

// CandidateGenerationResolution is the native publish binding resolved from
// one candidate row. GenerationCount is retained so callers can fail closed
// when malformed history associates a candidate with more than one generation.
type CandidateGenerationResolution struct {
	CandidateID       string
	TargetID          string
	PlanID            string
	SnapshotSealID    string
	Status            string
	CandidateRevision int64
	ArtifactDigest    string
	ProjectID         string
	Environment       string
	GenerationCount   int64
	GenerationID      string
}

type DeliveryGeneration struct {
	GenerationID, TargetID, CandidateID, SnapshotSealID, PlanID          string
	PlanDigest, ArtifactRoot, ArtifactRootDigest, ServingArtifactDigest  string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint string
	GenerationRevision                                                   int64
	CreatedAt                                                            time.Time
}
type GenerationInput = DeliveryGeneration

type DeliveryPublication struct {
	PublicationID, TargetID, GenerationID, ExpectedBaseGenerationID, CandidateID, SnapshotSealID string
	ExpectedTargetRevision, ResultTargetRevision                                                 int64
	ActorID, State, RequestDigest                                                                string
	CreatedAt, CommittedAt                                                                       time.Time
}
type PublicationInput = DeliveryPublication

type DeliveryLease struct {
	LeaseID, TargetID, OwnerID        string
	FencingEpoch                      int64
	State                             string
	ExpiresAt, AcquiredAt, ReleasedAt time.Time
}
type LeaseInput struct {
	LeaseID, TargetID, OwnerID string
	ExpiresAt                  time.Time
}
type LeaseFence struct {
	LeaseID, TargetID, OwnerID string
	FencingEpoch               int64
}

// CompleteBuildResult is the durable PostgreSQL evidence produced by a
// completed build. The lease is returned in its post-release state so callers
// can append their own event/audit/workflow consequences in the same
// transaction without having to issue another read.
type CompleteBuildResult struct {
	Attempt   DeliveryBuildAttempt
	Seal      SnapshotSeal
	Candidate DeliveryCandidate
	Lease     DeliveryLease
}

type DeliveryRetentionRoot struct {
	RootID, TargetID, CandidateID, GenerationID, SnapshotSealID, RootKind, State string
	ExpiresAt, CreatedAt, RetiredAt, ExpiredAt                                   time.Time
	Evidence                                                                     json.RawMessage
}

func loadRetentionRoot(ctx context.Context, db DBTX, id string) (DeliveryRetentionRoot, error) {
	var r DeliveryRetentionRoot
	row, err := depdb.New(db).GetRetentionRoot(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryRetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	r.RootID, r.TargetID, r.CandidateID, r.GenerationID, r.SnapshotSealID, r.RootKind, r.State = row.RootID, row.TargetID, row.CandidateID, row.GenerationID, row.SnapshotSealID, row.RootKind, row.State
	if row.ExpiresAt.Valid {
		r.ExpiresAt = row.ExpiresAt.Time.UTC()
	}
	if row.RetiredAt.Valid {
		r.RetiredAt = row.RetiredAt.Time.UTC()
	}
	if row.ExpiredAt.Valid {
		r.ExpiredAt = row.ExpiredAt.Time.UTC()
	}
	r.CreatedAt, r.Evidence = dbTime(row.CreatedAt), append([]byte(nil), row.Evidence...)
	return r, nil
}

// RequireGenerationRootTx proves that a rollback target remains retained by
// the delivery authority. Each activation owns an immutable generation-root
// identity; any exact live/retiring root is eligible, while expired or missing
// roots fail closed.
func (r *Repository) RequireGenerationRootTx(ctx context.Context, tx Tx, targetID, generationID string) error {
	if tx == nil {
		return ErrInvalid
	}
	target, err := textID(targetID, "target id")
	if err != nil {
		return err
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return err
	}
	found, err := depdb.New(tx).FindRetainedGenerationRoot(contextOrBackground(ctx), depdb.FindRetainedGenerationRootParams{TargetID: target, GenerationID: dbUUID(generation)})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: rollback generation retention root is unavailable", ErrConflict)
	}
	if err != nil {
		return err
	}
	root, err := depdb.New(tx).GetRetentionRootIdentity(contextOrBackground(ctx), dbUUID(found.RootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: rollback generation retention root is unavailable", ErrConflict)
	}
	if err != nil {
		return err
	}
	if root.TargetID != target || root.GenerationID != generation || root.RootKind != "generation" || (root.State != "live" && root.State != "retiring") {
		return fmt.Errorf("%w: rollback generation retention root is unavailable", ErrConflict)
	}
	return nil
}

// RequireRollbackRootTx proves that a pending rollback publication established
// its own immutable reachability root. The root is keyed by publication ID so
// replay cannot silently proceed after that protection has been removed or
// expired.
func (r *Repository) RequireRollbackRootTx(ctx context.Context, tx Tx, rootID, targetID, generationID, candidateID, sealID string) error {
	if tx == nil {
		return ErrInvalid
	}
	root, err := uuidID(rootID, "rollback root id", false)
	if err != nil {
		return err
	}
	target, err := textID(targetID, "target id")
	if err != nil {
		return err
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return err
	}
	candidate, err := uuidID(candidateID, "candidate id", false)
	if err != nil {
		return err
	}
	seal, err := uuidID(sealID, "snapshot seal id", false)
	if err != nil {
		return err
	}
	row, err := depdb.New(tx).GetRetentionRootIdentity(contextOrBackground(ctx), dbUUID(root))
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: rollback publication retention root is unavailable", ErrConflict)
	}
	if err != nil {
		return err
	}
	if row.TargetID != target || row.GenerationID != generation || row.CandidateID != candidate || row.SnapshotSealID != seal || row.RootKind != "rollback" || row.State != "live" {
		return fmt.Errorf("%w: rollback publication retention root is unavailable", ErrConflict)
	}
	if row.ExpiresAt.Valid {
		now, clockErr := databaseNow(contextOrBackground(ctx), tx)
		if clockErr != nil {
			return clockErr
		}
		if !row.ExpiresAt.Time.After(now) {
			return fmt.Errorf("%w: rollback publication retention root is unavailable", ErrConflict)
		}
	}
	return nil
}

type Event struct {
	EventID, ScopeID, AggregateType, AggregateID string
	AggregateVersion                             int64
	EventType                                    string
	SchemaVersion                                int64
	OccurredAt                                   time.Time
	CorrelationID                                string
	Payload                                      json.RawMessage
}

type AuditEvent struct {
	AuditID, EventID, ScopeID, ActorID, Action, ResourceKind, ResourceID, Outcome string
	RequestDigest                                                                 string
	Metadata                                                                      json.RawMessage
	OccurredAt                                                                    time.Time
}

// ActivationAuditInput is the deployment-owned projection of the canonical
// activation audit identity. The audit adapter receives the caller-owned
// transaction and must append/read the access-owned row without taking
// transaction ownership itself. EventID is also the audit retry identity.
//
// Outcome is expressed in deployment terms ("accepted"); the composition
// adapter maps it to the access canonical outcome ("success") and maps it
// back on read. Keeping this input here prevents deployment persistence from
// depending on the access package's intent shape.
type ActivationAuditInput struct {
	EventID, DomainEventID, ScopeID, ActorID   string
	Action, ResourceKind, ResourceID, Outcome  string
	RequestDigest, CorrelationID, AggregateKey string
	AggregateSequence                          int64
	Metadata                                   json.RawMessage
}

// ActivationAuditPort is the narrow composition seam for activation audit
// evidence. Both methods use the transaction supplied by deployment; they
// must never begin, commit, or roll back it. Implementations must return
// ErrConflict for an existing identity whose canonical fields differ or for
// missing/tampered replay evidence.
type ActivationAuditPort interface {
	AppendActivationAudit(context.Context, Tx, ActivationAuditInput) (AuditEvent, error)
	GetActivationAudit(context.Context, Tx, ActivationAuditInput) (AuditEvent, error)
}

// ActivationLineageInput is the canonical delivery identity used to verify
// the immutable compiler lineage projection before activation. TargetID is
// the delivery target (never a build-operation or commit-marker ID).
type ActivationLineageInput struct {
	TargetID, ProjectID, GenerationID string
}

// ActivationLineageVerifier is the narrow composition seam for activation's
// immutable lineage evidence. Implementations must read through the supplied
// caller-owned transaction and return an error when the exact target/project/
// generation binding is absent or does not verify.
type ActivationLineageVerifier interface {
	VerifyActivationLineage(context.Context, Tx, ActivationLineageInput) error
}

// Options wires deployment's transactional side effects. The audit and
// lineage ports are required by activation; other delivery operations remain
// usable without them for isolated persistence tests.
type Options struct {
	ActivationAudit ActivationAuditPort
	Lineage         ActivationLineageVerifier
	// Events is the exact canonical repository used to append and read
	// activation event evidence.
	Events *eventspostgres.Repository
}

// ActivationInput is the complete fence and compare-and-swap proof for one
// publication.  LeaseID/OwnerID/FencingEpoch are mandatory: an expired lease
// or stale owner can never advance the active pointer.
type ActivationInput struct {
	PublicationID, TargetID, GenerationID string
	ExpectedTargetRevision                int64
	RequestDigest, ActorID, CorrelationID string
	LeaseID, OwnerID                      string
	FencingEpoch                          int64
}

// ActivationPreCommitHook is an optional composition-owned interruption seam
// invoked after every durable activation proof has been checked while the
// publication and target remain locked, but before the target CAS is mutated.
// Production leaves it nil; release qualification uses it to prove restart
// recovery at the exact pre-commit boundary.
type ActivationPreCommitHook func(context.Context, DeliveryPublication) error

type ActivationResult struct {
	Publication DeliveryPublication
	Pointer     DeliveryTarget
	Event       Event
	Audit       AuditEvent
	Replay      bool
}

type Repository struct {
	db      DBTX
	audit   ActivationAuditPort
	lineage ActivationLineageVerifier
	events  *eventspostgres.Repository
}

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
	// sqlc-exception: schema-ddl. Capability-owned schema DDL is applied as a
	// single caller-owned migration transaction.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository { return newRepository(db, Options{}) }

// NewMaintenance constructs the bounded delivery-root retention facade.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

// NewWithOptions constructs a delivery repository with its composition-owned
// activation authorities. Missing authorities are allowed for read/build-only
// repository use, but Activate/ActivateTx fail closed when either is absent.
func NewWithOptions(db DBTX, options Options) *Repository {
	return newRepository(db, options)
}

func newRepository(db DBTX, options Options) *Repository {
	events := options.Events
	// Preserve the ergonomic low-level constructors used by persistence tests,
	// while production composition passes the exact application-owned event
	// repository explicitly.
	if events == nil {
		events = eventspostgres.New()
	}
	return &Repository{db: db, audit: options.ActivationAudit, lineage: options.Lineage, events: events}
}

// DB exposes the configured native PostgreSQL handle to composition-owned
// adapters (for example Access audit and jobs workflow).  The returned handle
// is never used to begin a second transaction by those adapters; callers must
// pass the transaction returned by Begin to every side-effect port.
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

// Begin starts a caller-owned native control transaction.  Keeping the
// transaction constructor on the authority prevents callers from silently
// selecting another database or crossing into DuckLake.
func (r *Repository) Begin(ctx context.Context) (Tx, error) {
	return r.begin(ctx)
}

// PostgreSQLAuthority marks this repository as the clean-slate delivery
// authority.  The marker is intentionally implemented only by this concrete
// repository; module composition uses it together with Configured and
// AuditCapable to reject a database/sql or SQLite implementation.
func (*Repository) PostgreSQLAuthority() {}

// Configured reports whether the repository has a native database handle.
// Schema readiness remains the migration/lifecycle owner's responsibility.
func (r *Repository) Configured() bool { return r != nil && nativeDBConfigured(r.db) }

// TransactionCapable reports whether the native handle can begin the
// caller-owned control-plane transactions required by activation, leasing,
// and atomic candidate admission.
func (r *Repository) TransactionCapable() bool {
	if r == nil || !nativeDBConfigured(r.db) {
		return false
	}
	_, ok := r.db.(beginner)
	return ok
}

// AuditCapable reports whether activation can append its audit evidence in
// the same caller-owned PostgreSQL transaction.
func (r *Repository) AuditCapable() bool {
	return r != nil && r.audit != nil
}

// EventCapable reports whether activation has the canonical event authority.
func (r *Repository) EventCapable() bool {
	return r != nil && r.events != nil
}

func dbUUID(value string) pgtype.UUID {
	u, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

// dbTime converts sqlc's pgx/v5 timestamptz representation into the
// repository's time.Time surface. The canonical sqlc configuration maps all
// PostgreSQL timestamptz columns to pgtype.Timestamptz, including non-null
// columns; nullable values retain their Valid bit for callers that need to
// distinguish an absent timestamp.
func dbTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func pgTime(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func pgText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func pgInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func databaseNow(ctx context.Context, db DBTX) (time.Time, error) {
	now, err := depdb.New(db).DatabaseClock(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return dbTime(now), nil
}

// DatabaseNowTx returns the PostgreSQL authority clock through a
// caller-owned transaction. Transactional coordinators use this when they
// need to derive a bounded lease deadline; application/node clocks must not
// become durable lease authority.
func (r *Repository) DatabaseNowTx(ctx context.Context, tx Tx) (time.Time, error) {
	if tx == nil {
		return time.Time{}, ErrInvalid
	}
	return databaseNow(contextOrBackground(ctx), tx)
}

// DatabaseNow returns the authoritative PostgreSQL clock for callers that
// need to derive a bounded request deadline before opening their mutation
// transaction. Transactional mutations should prefer DatabaseNowTx.
func (r *Repository) DatabaseNow(ctx context.Context) (time.Time, error) {
	db, err := requireDB(r)
	if err != nil {
		return time.Time{}, err
	}
	return databaseNow(contextOrBackground(ctx), db)
}

func requireDB(r *Repository) (DBTX, error) {
	if r == nil || !nativeDBConfigured(r.db) {
		return nil, ErrInvalid
	}
	return r.db, nil
}

func nativeDBConfigured(db DBTX) bool {
	if db == nil {
		return false
	}
	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func uuidID(value, label string, generate bool) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" && generate {
		u, err := uuid.NewV7()
		if err != nil {
			return "", err
		}
		return u.String(), nil
	}
	if v == "" || v != value {
		return "", fmt.Errorf("%w: %s must be a UUID", ErrInvalid, label)
	}
	u, err := uuid.Parse(v)
	if err != nil {
		return "", fmt.Errorf("%w: %s must be a UUID", ErrInvalid, label)
	}
	return u.String(), nil
}

func textID(value, label string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxText || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%w: %s is invalid", ErrInvalid, label)
	}
	return value, nil
}

func digest(value, label string) (string, error) {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return "", fmt.Errorf("%w: %s must be sha256 digest", ErrInvalid, label)
	}
	for _, c := range value[7:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", fmt.Errorf("%w: %s must be lowercase sha256 digest", ErrInvalid, label)
		}
	}
	return value, nil
}

func canonicalObject(raw json.RawMessage, max int, allowEmpty bool) (json.RawMessage, error) {
	if len(raw) == 0 {
		if allowEmpty {
			return json.RawMessage(`{}`), nil
		}
		return nil, ErrInvalid
	}
	if len(raw) > max {
		return nil, ErrInvalid
	}
	var object map[string]any
	if err := strictjson.Decode(raw, &object); err != nil || object == nil || (!allowEmpty && len(object) == 0) {
		return nil, ErrInvalid
	}
	b, err := json.Marshal(object)
	if err != nil || len(b) > max {
		return nil, ErrInvalid
	}
	return b, nil
}

// canonicalPlanDocument validates and canonicalizes the complete rich
// deployment plan document. Plan creation requires the caller's bytes to be
// the canonical serialization emitted by deployment.NewDeliveryPlan; reads
// from PostgreSQL jsonb use normalizeStoredPlanDocument because jsonb may
// reorder object keys while preserving the same value.
func canonicalPlanDocument(raw json.RawMessage) (json.RawMessage, deployment.DeliveryPlan, error) {
	if len(raw) == 0 || len(raw) > maxPlanDocument {
		return nil, deployment.DeliveryPlan{}, ErrInvalid
	}
	var plan deployment.DeliveryPlan
	if err := strictjson.DecodeWithOptions(raw, &plan, strictjson.Options{MaxBytes: maxPlanDocument}); err != nil {
		return nil, deployment.DeliveryPlan{}, ErrInvalid
	}
	normalized, err := deployment.NewDeliveryPlan(plan)
	if err != nil {
		return nil, deployment.DeliveryPlan{}, fmt.Errorf("%w: plan document: %v", ErrInvalid, err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil || len(encoded) > maxPlanDocument || !bytes.Equal(raw, encoded) {
		return nil, deployment.DeliveryPlan{}, ErrInvalid
	}
	return encoded, normalized, nil
}

func normalizeStoredPlanDocument(raw json.RawMessage) (json.RawMessage, deployment.DeliveryPlan, error) {
	if len(raw) == 0 || len(raw) > maxPlanDocument {
		return nil, deployment.DeliveryPlan{}, ErrInvalid
	}
	var plan deployment.DeliveryPlan
	if err := strictjson.DecodeWithOptions(raw, &plan, strictjson.Options{MaxBytes: maxPlanDocument}); err != nil {
		return nil, deployment.DeliveryPlan{}, ErrInvalid
	}
	normalized, err := deployment.NewDeliveryPlan(plan)
	if err != nil {
		return nil, deployment.DeliveryPlan{}, fmt.Errorf("%w: plan document: %v", ErrInvalid, err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil || len(encoded) > maxPlanDocument {
		return nil, deployment.DeliveryPlan{}, ErrInvalid
	}
	return encoded, normalized, nil
}

func planDocumentProjectionMatches(plan deployment.DeliveryPlan, in PlanInput) bool {
	// ArtifactDigest is the immutable packed serving-bundle identity retained
	// by the delivery row. PostgreSQL plans must carry this identity explicitly
	// in the rich plan; a source digest is a different identity and is never a
	// valid substitute.
	return plan.ID == in.PlanID && plan.TargetID == in.TargetID &&
		plan.Digest == in.PlanDigest && plan.Execution.ConfigDigest == in.CompiledConfigDigest &&
		plan.ServingArtifactDigest == in.ArtifactDigest &&
		plan.Governance.AuthorizationDigest == in.SecurityDomainFingerprint &&
		plan.Governance.QualificationDigest == in.QualificationDigest &&
		plan.Governance.RequiresApproval == in.ApprovalRequired &&
		plan.Governance.ApprovalPolicyRevision == in.ApprovalPolicyRevision
}

func validatePlanTargetScope(ctx context.Context, db DBTX, plan DeliveryPlan) error {
	richPlan, err := plan.RichPlan()
	if err != nil {
		return err
	}
	target, err := loadTarget(ctx, db, plan.TargetID)
	if err != nil {
		return err
	}
	if richPlan.ProjectID.String() != target.ProjectID || richPlan.Environment != target.Environment {
		return fmt.Errorf("%w: plan document scope differs from target", ErrConflict)
	}
	return nil
}

func samePlanDocument(a, b []byte) bool {
	left, _, leftErr := normalizeStoredPlanDocument(a)
	right, _, rightErr := normalizeStoredPlanDocument(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// RichPlan rehydrates the complete canonical deployment contract retained in
// PlanDocument. The same strict decoding and projection checks used by reads
// are applied again so native orchestration cannot accidentally execute an
// unvalidated document supplied by a caller.
func (p DeliveryPlan) RichPlan() (deployment.DeliveryPlan, error) {
	_, richPlan, err := normalizeStoredPlanDocument(p.PlanDocument)
	if err != nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: plan document is invalid", ErrConflict)
	}
	if !planDocumentProjectionMatches(richPlan, PlanInput{
		PlanID: p.PlanID, TargetID: p.TargetID, PlanDigest: p.PlanDigest,
		CompiledConfigDigest: p.CompiledConfigDigest, SecurityDomainFingerprint: p.SecurityDomainFingerprint,
		ArtifactDigest: p.ArtifactDigest, QualificationDigest: p.QualificationDigest,
		ApprovalRequired: p.ApprovalRequired, ApprovalPolicyRevision: p.ApprovalPolicyRevision,
	}) {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: plan projections differ from plan document", ErrConflict)
	}
	return richPlan, nil
}

func mapPlanRow(row depdb.GetPlanRow) (DeliveryPlan, error) {
	document, richPlan, err := normalizeStoredPlanDocument(row.PlanDocument)
	if err != nil {
		return DeliveryPlan{}, fmt.Errorf("%w: persisted plan document is invalid", ErrConflict)
	}
	p := DeliveryPlan{
		PlanID: row.PlanID, TargetID: row.TargetID, PlanRevision: row.PlanRevision,
		PlanDigest: row.PlanDigest, CompiledGraphDigest: row.CompiledGraphDigest,
		CompiledConfigDigest: row.CompiledConfigDigest, SecurityDomainFingerprint: row.SecurityDomainFingerprint,
		ArtifactDigest: row.ArtifactDigest, QualificationDigest: row.QualificationDigest,
		QualificationRequired: row.QualificationRequired, ApprovalRequired: row.ApprovalRequired, ApprovalPolicyRevision: row.ApprovalPolicyRevision, PlanDocument: append([]byte(nil), document...),
		Evidence: append([]byte(nil), row.Evidence...), CreatedAt: dbTime(row.CreatedAt),
	}
	if !planDocumentProjectionMatches(richPlan, PlanInput{
		PlanID: p.PlanID, TargetID: p.TargetID, PlanDigest: p.PlanDigest,
		CompiledConfigDigest: p.CompiledConfigDigest, SecurityDomainFingerprint: p.SecurityDomainFingerprint,
		ArtifactDigest: p.ArtifactDigest, QualificationDigest: p.QualificationDigest,
		QualificationRequired: p.QualificationRequired,
		ApprovalRequired:      p.ApprovalRequired, ApprovalPolicyRevision: p.ApprovalPolicyRevision,
	}) {
		return DeliveryPlan{}, fmt.Errorf("%w: persisted plan projections differ from plan document", ErrConflict)
	}
	return p, nil
}

func canonicalNonEmpty(raw json.RawMessage, max int) (json.RawMessage, error) {
	return canonicalObject(raw, max, false)
}

// BuildAttemptMarkerResolutionEvidence is the bounded, typed proof that an
// exact marker lookup completed and found no matching snapshot. It is
// intentionally separate from session-termination evidence: absence of a
// marker does not prove the external transaction cannot still commit. Every
// physical identity is repeated in the document so a resolution cannot be
// replayed for another pool, catalog, request, plan, or attempt.
type BuildAttemptMarkerResolutionEvidence struct {
	SchemaVersion  int       `json:"schema_version"`
	PhysicalPoolID string    `json:"physical_pool_id"`
	CatalogID      string    `json:"catalog_id"`
	AttemptID      string    `json:"attempt_id"`
	RequestDigest  string    `json:"request_digest"`
	PlanDigest     string    `json:"plan_digest"`
	MarkerAbsent   bool      `json:"marker_absent"`
	ResolvedAt     time.Time `json:"resolved_at"`
}

func normalizeSuccessorResolutionEvidence(raw json.RawMessage, predecessor DeliveryBuildAttempt, catalogID string) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxEvidence {
		return nil, fmt.Errorf("%w: physical marker resolution evidence is required", ErrInvalid)
	}
	var evidence BuildAttemptMarkerResolutionEvidence
	if err := strictjson.DecodeWithOptions(raw, &evidence, strictjson.Options{MaxBytes: maxEvidence, MaxDepth: 32, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil {
		return nil, fmt.Errorf("%w: physical marker resolution evidence is invalid", ErrInvalid)
	}
	if evidence.SchemaVersion != 1 || evidence.PhysicalPoolID != predecessor.PhysicalPoolID || evidence.CatalogID != catalogID || evidence.AttemptID != predecessor.AttemptID || evidence.RequestDigest != predecessor.RequestDigest || evidence.PlanDigest != predecessor.PlanDigest || !evidence.MarkerAbsent || evidence.ResolvedAt.IsZero() {
		return nil, fmt.Errorf("%w: physical marker resolution identity differs from predecessor", ErrConflict)
	}
	if evidence.ResolvedAt.Location() != time.UTC {
		return nil, fmt.Errorf("%w: physical marker resolution timestamp must be UTC", ErrInvalid)
	}
	canonical, err := json.Marshal(evidence)
	if err != nil || len(canonical) > maxEvidence || !bytes.Equal(raw, canonical) {
		return nil, fmt.Errorf("%w: physical marker resolution evidence is not canonical", ErrInvalid)
	}
	return canonical, nil
}

func sameBytes(a, b []byte) bool { return bytes.Equal(a, b) }

func (r *Repository) begin(ctx context.Context) (pgx.Tx, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	b, ok := db.(beginner)
	if !ok {
		return nil, errors.New("delivery repository requires transaction-capable PostgreSQL DB")
	}
	return b.Begin(contextOrBackground(ctx))
}

// CreateTarget creates (or exactly replays) one project/environment target.
func (r *Repository) CreateTarget(ctx context.Context, in TargetInput) (DeliveryTarget, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryTarget{}, err
	}
	return createTarget(contextOrBackground(ctx), db, in)
}

// CreateTargetTx creates (or exactly replays) one project/environment target
// through a caller-owned PostgreSQL transaction.  The method deliberately
// does not commit or roll back tx so callers can compose target admission with
// the first plan and their own audit/workflow consequences.
func (r *Repository) CreateTargetTx(ctx context.Context, tx Tx, in TargetInput) (DeliveryTarget, error) {
	if tx == nil {
		return DeliveryTarget{}, ErrInvalid
	}
	return createTarget(contextOrBackground(ctx), tx, in)
}

func createTarget(ctx context.Context, db DBTX, in TargetInput) (DeliveryTarget, error) {
	id, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	project, err := textID(in.ProjectID, "project id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	env, err := textID(in.Environment, "environment")
	if err != nil {
		return DeliveryTarget{}, err
	}
	rev := in.TargetRevision
	if rev == 0 {
		rev = 1
	}
	if rev < 1 {
		return DeliveryTarget{}, ErrInvalid
	}
	err = depdb.New(db).InsertTarget(ctx, depdb.InsertTargetParams{TargetID: id, ProjectID: project, Environment: env, TargetRevision: rev})
	if err != nil {
		return DeliveryTarget{}, err
	}
	if err := depdb.New(db).InsertTargetFence(ctx, id); err != nil {
		return DeliveryTarget{}, err
	}
	g, err := loadTarget(ctx, db, id)
	if err != nil {
		return DeliveryTarget{}, err
	}
	if g.ProjectID != project || g.Environment != env || g.TargetRevision != rev {
		return DeliveryTarget{}, fmt.Errorf("%w: target identity differs", ErrConflict)
	}
	return g, nil
}

func (r *Repository) Target(ctx context.Context, id string) (DeliveryTarget, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryTarget{}, err
	}
	return loadTarget(contextOrBackground(ctx), db, id)
}
func (r *Repository) LoadTarget(ctx context.Context, id string) (DeliveryTarget, error) {
	return r.Target(ctx, id)
}

// OperatorSnapshot returns the bounded native operator projection for one
// target. Detail tables owned by other authorities (retention roots, query
// leases, and garbage-collection state) are intentionally not fabricated.
func (r *Repository) OperatorSnapshot(ctx context.Context, targetID string) (DeliveryOperatorSnapshot, error) {
	target, err := r.Target(ctx, targetID)
	if err != nil {
		return DeliveryOperatorSnapshot{}, err
	}
	return DeliveryOperatorSnapshot{
		ProjectID: target.ProjectID, Environment: target.Environment,
		TargetID: target.TargetID, TargetRevision: target.TargetRevision,
		ActiveGenerationID:  target.ActiveGenerationID,
		ActivePublicationID: target.ActivePublicationID,
	}, nil
}

// ActiveGeneration resolves the generation selected by a target's durable
// active pointer. The pointer and generation are read through the generated
// query layer and the generation's target identity is checked before it is
// returned. An absent pointer is reported as ErrNotFound; callers must not
// infer an active generation from recency or from a caller-provided ID.
func (r *Repository) ActiveGeneration(ctx context.Context, targetID string) (DeliveryGeneration, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	targetID, err = textID(targetID, "target id")
	if err != nil {
		return DeliveryGeneration{}, err
	}
	activeID, err := depdb.New(db).GetActiveGeneration(contextOrBackground(ctx), targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryGeneration{}, ErrNotFound
		}
		return DeliveryGeneration{}, err
	}
	if strings.TrimSpace(activeID) == "" {
		return DeliveryGeneration{}, ErrNotFound
	}
	activeID, err = uuidID(activeID, "active generation id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	generation, err := loadGeneration(contextOrBackground(ctx), db, activeID, GenerationInput{})
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if generation.GenerationID != activeID || generation.TargetID != targetID {
		return DeliveryGeneration{}, fmt.Errorf("%w: active generation target identity differs", ErrConflict)
	}
	return generation, nil
}

func loadTarget(ctx context.Context, db DBTX, id string) (DeliveryTarget, error) {
	id, err := textID(id, "target id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	var out DeliveryTarget
	row, err := depdb.New(db).GetTarget(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTarget{}, ErrNotFound
	}
	if err != nil {
		return DeliveryTarget{}, err
	}
	out.TargetID, out.ProjectID, out.Environment, out.TargetRevision, out.ActiveGenerationID, out.ActivePublicationID, out.CreatedAt, out.UpdatedAt = row.TargetID, row.ProjectID, row.Environment, row.TargetRevision, row.ActiveGenerationID, row.ActivePublicationID, dbTime(row.CreatedAt), dbTime(row.UpdatedAt)
	return out, nil
}

// CreatePlan persists immutable compiler and governance identity.
func (r *Repository) CreatePlan(ctx context.Context, in PlanInput) (DeliveryPlan, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryPlan{}, err
	}
	return createPlan(contextOrBackground(ctx), db, in)
}

// CreatePlanTx persists (or exactly replays) immutable compiler and
// governance identity through a caller-owned PostgreSQL transaction.  It is
// intended to run in the same transaction as CreateTargetTx when a fresh
// target is admitted for its first plan.
func (r *Repository) CreatePlanTx(ctx context.Context, tx Tx, in PlanInput) (DeliveryPlan, error) {
	if tx == nil {
		return DeliveryPlan{}, ErrInvalid
	}
	return createPlan(contextOrBackground(ctx), tx, in)
}

// CreatePlanAllocatedTx atomically admits a plan using the next revision
// owned by its target. The caller owns tx's commit/rollback. An existing plan
// UUID is replayed before allocating a revision and must match all immutable
// input fields exactly.
func (r *Repository) CreatePlanAllocatedTx(ctx context.Context, tx Tx, in PlanInput) (DeliveryPlan, error) {
	if tx == nil {
		return DeliveryPlan{}, ErrInvalid
	}
	return createPlanAllocated(contextOrBackground(ctx), tx, in)
}

// CreatePlanAllocated owns a short transaction around CreatePlanAllocatedTx.
func (r *Repository) CreatePlanAllocated(ctx context.Context, in PlanInput) (DeliveryPlan, error) {
	tx, err := r.begin(contextOrBackground(ctx))
	if err != nil {
		return DeliveryPlan{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	out, err := r.CreatePlanAllocatedTx(ctx, tx, in)
	if err != nil {
		return DeliveryPlan{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryPlan{}, err
	}
	return out, nil
}

// CreateTargetAndPlanTx atomically creates (or exactly replays) a target and
// its immutable plan through a caller-owned PostgreSQL transaction.  The
// caller owns the final commit/rollback; on any error no mutation is
// committed by this method.
func (r *Repository) CreateTargetAndPlanTx(ctx context.Context, tx Tx, target TargetInput, plan PlanInput) (DeliveryTarget, DeliveryPlan, error) {
	if tx == nil {
		return DeliveryTarget{}, DeliveryPlan{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	createdTarget, err := createTarget(ctx, tx, target)
	if err != nil {
		return DeliveryTarget{}, DeliveryPlan{}, err
	}
	if plan.TargetID != createdTarget.TargetID {
		return DeliveryTarget{}, DeliveryPlan{}, fmt.Errorf("%w: plan target differs from target", ErrConflict)
	}
	createdPlan, err := createPlan(ctx, tx, plan)
	if err != nil {
		return DeliveryTarget{}, DeliveryPlan{}, err
	}
	return createdTarget, createdPlan, nil
}

// CreateTargetAndPlanAllocatedTx atomically admits a fresh target and its
// first plan. The target row and target-owned plan revision are committed (or
// rolled back) together by the caller.
func (r *Repository) CreateTargetAndPlanAllocatedTx(ctx context.Context, tx Tx, target TargetInput, plan PlanInput) (DeliveryTarget, DeliveryPlan, error) {
	if tx == nil {
		return DeliveryTarget{}, DeliveryPlan{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	createdTarget, err := createTarget(ctx, tx, target)
	if err != nil {
		return DeliveryTarget{}, DeliveryPlan{}, err
	}
	if plan.TargetID != createdTarget.TargetID {
		return DeliveryTarget{}, DeliveryPlan{}, fmt.Errorf("%w: plan target differs from target", ErrConflict)
	}
	createdPlan, err := createPlanAllocated(ctx, tx, plan)
	if err != nil {
		return DeliveryTarget{}, DeliveryPlan{}, err
	}
	return createdTarget, createdPlan, nil
}

func createPlan(ctx context.Context, db DBTX, in PlanInput) (DeliveryPlan, error) {
	if in.ApprovalPolicyRevision < 1 {
		return DeliveryPlan{}, ErrInvalid
	}
	id, err := uuidID(in.PlanID, "plan id", true)
	if err != nil {
		return DeliveryPlan{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryPlan{}, err
	}
	if in.PlanRevision <= 0 {
		return DeliveryPlan{}, ErrInvalid
	}
	for label, value := range map[string]string{"plan digest": in.PlanDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "artifact digest": in.ArtifactDigest, "qualification digest": in.QualificationDigest} {
		if _, err := digest(value, label); err != nil {
			return DeliveryPlan{}, err
		}
	}
	evidence, err := canonicalObject(in.Evidence, 65536, true)
	if err != nil {
		return DeliveryPlan{}, fmt.Errorf("%w: plan evidence", ErrInvalid)
	}
	planDocument, richPlan, err := canonicalPlanDocument(in.PlanDocument)
	if err != nil {
		return DeliveryPlan{}, fmt.Errorf("%w: plan document", ErrInvalid)
	}
	if !planDocumentProjectionMatches(richPlan, in) {
		return DeliveryPlan{}, fmt.Errorf("%w: plan projections differ from plan document", ErrConflict)
	}
	targetRow, err := loadTarget(ctx, db, target)
	if err != nil {
		return DeliveryPlan{}, err
	}
	if richPlan.ProjectID.String() != targetRow.ProjectID || richPlan.Environment != targetRow.Environment {
		return DeliveryPlan{}, fmt.Errorf("%w: plan document scope differs from target", ErrConflict)
	}
	err = depdb.New(db).InsertPlan(ctx, depdb.InsertPlanParams{PlanID: dbUUID(id), TargetID: target, PlanRevision: in.PlanRevision, PlanDigest: in.PlanDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, ArtifactDigest: in.ArtifactDigest, QualificationDigest: in.QualificationDigest, QualificationRequired: in.QualificationRequired, ApprovalRequired: in.ApprovalRequired, ApprovalPolicyRevision: in.ApprovalPolicyRevision, PlanDocument: planDocument, Evidence: evidence})
	if err != nil {
		return DeliveryPlan{}, err
	}
	return loadPlan(ctx, db, id, in)
}

func planAllocationInput(in PlanInput) (id, target string, evidence, planDocument []byte, err error) {
	if in.PlanRevision != 0 {
		return "", "", nil, nil, ErrInvalid
	}
	id, err = uuidID(in.PlanID, "plan id", true)
	if err != nil {
		return "", "", nil, nil, err
	}
	target, err = textID(in.TargetID, "target id")
	if err != nil {
		return "", "", nil, nil, err
	}
	for label, value := range map[string]string{"plan digest": in.PlanDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "artifact digest": in.ArtifactDigest, "qualification digest": in.QualificationDigest} {
		if _, err := digest(value, label); err != nil {
			return "", "", nil, nil, err
		}
	}
	evidence, err = canonicalObject(in.Evidence, 65536, true)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("%w: plan evidence", ErrInvalid)
	}
	planDocument, richPlan, documentErr := canonicalPlanDocument(in.PlanDocument)
	if documentErr != nil {
		return "", "", nil, nil, fmt.Errorf("%w: plan document", ErrInvalid)
	}
	if !planDocumentProjectionMatches(richPlan, in) {
		return "", "", nil, nil, fmt.Errorf("%w: plan projections differ from plan document", ErrConflict)
	}
	return id, target, evidence, planDocument, nil
}

func planImmutableMatches(p DeliveryPlan, in PlanInput, target, id string, evidence []byte) bool {
	return p.PlanID == id && p.TargetID == target && p.PlanDigest == in.PlanDigest &&
		p.CompiledGraphDigest == in.CompiledGraphDigest && p.CompiledConfigDigest == in.CompiledConfigDigest &&
		p.SecurityDomainFingerprint == in.SecurityDomainFingerprint && p.ArtifactDigest == in.ArtifactDigest && p.QualificationDigest == in.QualificationDigest &&
		p.QualificationRequired == in.QualificationRequired && p.ApprovalRequired == in.ApprovalRequired && p.ApprovalPolicyRevision == in.ApprovalPolicyRevision &&
		sameCanonical(p.Evidence, evidence) && samePlanDocument(p.PlanDocument, in.PlanDocument)
}

func loadPlanForAllocation(ctx context.Context, db DBTX, id, target string, in PlanInput, evidence []byte) (DeliveryPlan, error) {
	row, err := depdb.New(db).GetPlan(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p, mapErr := mapPlanRow(row)
	if mapErr != nil {
		return DeliveryPlan{}, mapErr
	}
	if scopeErr := validatePlanTargetScope(ctx, db, p); scopeErr != nil {
		return DeliveryPlan{}, scopeErr
	}
	if !planImmutableMatches(p, in, target, id, evidence) {
		return DeliveryPlan{}, ErrConflict
	}
	return p, nil
}

func createPlanAllocated(ctx context.Context, db DBTX, in PlanInput) (DeliveryPlan, error) {
	if in.ApprovalPolicyRevision < 1 {
		return DeliveryPlan{}, ErrInvalid
	}
	id, target, evidence, planDocument, err := planAllocationInput(in)
	if err != nil {
		return DeliveryPlan{}, err
	}
	q := depdb.New(db)
	if err := q.EnsureTargetRevision(ctx, target); err != nil {
		return DeliveryPlan{}, err
	}
	if _, err := q.LockTargetRevision(ctx, target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryPlan{}, ErrNotFound
		}
		return DeliveryPlan{}, err
	}
	richPlan, richPlanErr := DeliveryPlan{PlanID: id, TargetID: target, PlanDigest: in.PlanDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, ArtifactDigest: in.ArtifactDigest, QualificationDigest: in.QualificationDigest, ApprovalRequired: in.ApprovalRequired, ApprovalPolicyRevision: in.ApprovalPolicyRevision, PlanDocument: planDocument}.RichPlan()
	if richPlanErr != nil {
		return DeliveryPlan{}, richPlanErr
	}
	targetRow, targetErr := loadTarget(ctx, db, target)
	if targetErr != nil {
		return DeliveryPlan{}, targetErr
	}
	if richPlan.ProjectID.String() != targetRow.ProjectID || richPlan.Environment != targetRow.Environment {
		return DeliveryPlan{}, fmt.Errorf("%w: plan document scope differs from target", ErrConflict)
	}
	if existing, lookupErr := loadPlanForAllocation(ctx, db, id, target, in, evidence); lookupErr == nil {
		return existing, nil
	} else if !errors.Is(lookupErr, ErrNotFound) {
		return DeliveryPlan{}, lookupErr
	}
	revision, err := q.NextPlanRevision(ctx, target)
	if err != nil {
		return DeliveryPlan{}, err
	}
	if err := q.InsertPlan(ctx, depdb.InsertPlanParams{PlanID: dbUUID(id), TargetID: target, PlanRevision: revision, PlanDigest: in.PlanDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, ArtifactDigest: in.ArtifactDigest, QualificationDigest: in.QualificationDigest, QualificationRequired: in.QualificationRequired, ApprovalRequired: in.ApprovalRequired, ApprovalPolicyRevision: in.ApprovalPolicyRevision, PlanDocument: planDocument, Evidence: evidence}); err != nil {
		return DeliveryPlan{}, err
	}
	allocated := in
	allocated.PlanID, allocated.TargetID, allocated.PlanRevision, allocated.PlanDocument, allocated.Evidence = id, target, revision, planDocument, evidence
	return loadPlan(ctx, db, id, allocated)
}
func loadPlan(ctx context.Context, db DBTX, id string, expected PlanInput) (DeliveryPlan, error) {
	row, err := depdb.New(db).GetPlan(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p, mapErr := mapPlanRow(row)
	if mapErr != nil {
		return DeliveryPlan{}, mapErr
	}
	if scopeErr := validatePlanTargetScope(ctx, db, p); scopeErr != nil {
		return DeliveryPlan{}, scopeErr
	}
	if expected.ApprovalPolicyRevision < 1 {
		return DeliveryPlan{}, ErrInvalid
	}
	expectedEvidence, _ := canonicalObject(expected.Evidence, 65536, true)
	if p.TargetID != expected.TargetID || p.PlanRevision != expected.PlanRevision || p.PlanDigest != expected.PlanDigest || p.CompiledGraphDigest != expected.CompiledGraphDigest || p.SecurityDomainFingerprint != expected.SecurityDomainFingerprint || p.ArtifactDigest != expected.ArtifactDigest || p.QualificationDigest != expected.QualificationDigest || p.QualificationRequired != expected.QualificationRequired || p.ApprovalRequired != expected.ApprovalRequired || p.ApprovalPolicyRevision != expected.ApprovalPolicyRevision || !sameCanonical(p.Evidence, expectedEvidence) || !samePlanDocument(p.PlanDocument, expected.PlanDocument) {
		return DeliveryPlan{}, ErrConflict
	}
	return p, nil
}
func (r *Repository) Plan(ctx context.Context, id string) (DeliveryPlan, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryPlan{}, err
	}
	id, err = uuidID(id, "plan id", false)
	if err != nil {
		return DeliveryPlan{}, err
	}
	row, err := depdb.New(db).GetPlan(contextOrBackground(ctx), dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p, err := mapPlanRow(row)
	if err != nil {
		return DeliveryPlan{}, err
	}
	if err := validatePlanTargetScope(contextOrBackground(ctx), db, p); err != nil {
		return DeliveryPlan{}, err
	}
	return p, nil
}

// PlanTx reads immutable delivery-plan evidence through a caller-owned
// transaction. It is used by refresh publication adapters that must verify
// deployment state before committing their own authority transaction.
func (r *Repository) PlanTx(ctx context.Context, tx Tx, id string) (DeliveryPlan, error) {
	if tx == nil {
		return DeliveryPlan{}, ErrInvalid
	}
	id, err := uuidID(id, "plan id", false)
	if err != nil {
		return DeliveryPlan{}, err
	}
	row, err := depdb.New(tx).GetPlan(contextOrBackground(ctx), dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p, err := mapPlanRow(row)
	if err != nil {
		return DeliveryPlan{}, err
	}
	if err := validatePlanTargetScope(contextOrBackground(ctx), tx, p); err != nil {
		return DeliveryPlan{}, err
	}
	return p, nil
}
func (r *Repository) LoadPlan(ctx context.Context, id string) (DeliveryPlan, error) {
	return r.Plan(ctx, id)
}

// BeginBuildAttempt records the attempt before any DuckLake mutation.
func (r *Repository) BeginBuildAttempt(ctx context.Context, in BuildAttemptInput) (DeliveryBuildAttempt, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return beginBuildAttempt(contextOrBackground(ctx), db, in)
}

// BeginBuildAttemptTx records (or exactly replays) a build attempt through a
// caller-owned PostgreSQL transaction.  This is the transaction-bound form
// used when lease acquisition and attempt admission must share one commit
// boundary; it never commits or rolls back tx.
func (r *Repository) BeginBuildAttemptTx(ctx context.Context, tx Tx, in BuildAttemptInput) (DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	return beginBuildAttempt(contextOrBackground(ctx), tx, in)
}

// AdmitSuccessorBuildAttemptTx fences an indeterminate predecessor and
// admits a fresh successor in one caller-owned PostgreSQL transaction.  The
// predecessor must be bound to the exact lease fence supplied by the caller
// and already be indeterminate; its existing termination evidence is kept
// unchanged, its target lease is released, and a successor lease/attempt is
// then allocated. The successor receives a strictly higher target fencing
// epoch and an authority-derived relation namespace.
//
// This method deliberately does not infer termination from an expired lease.
// Callers must provide a bounded resolution document proving that the exact
// predecessor marker was absent. Positive session-termination evidence is a
// separate reconciliation outcome and is outside this successor primitive.
func (r *Repository) AdmitSuccessorBuildAttemptTx(ctx context.Context, tx Tx, in BuildAttemptSuccessorInput) (BuildAttemptSuccessorResult, error) {
	if tx == nil {
		return BuildAttemptSuccessorResult{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	predecessorID, err := uuidID(in.PredecessorAttemptID, "predecessor attempt id", false)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if in.Predecessor.LeaseID == "" || in.PredecessorAttemptID != predecessorID {
		return BuildAttemptSuccessorResult{}, ErrInvalid
	}
	if in.Predecessor.FencingEpoch <= 0 {
		return BuildAttemptSuccessorResult{}, ErrInvalid
	}
	// Lock the predecessor lease first, matching completion/recovery lock
	// ordering (lease -> attempt) and preventing a concurrent successor from
	// racing this normalization.
	predecessorLease, err := r.LockLeaseTx(ctx, tx, in.Predecessor.LeaseID)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if predecessorLease.TargetID != in.Predecessor.TargetID || predecessorLease.OwnerID != in.Predecessor.OwnerID || predecessorLease.FencingEpoch != in.Predecessor.FencingEpoch {
		return BuildAttemptSuccessorResult{}, ErrStaleFence
	}
	predecessor, err := r.BuildAttemptTx(ctx, tx, predecessorID)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if predecessor.OwnerID != in.Predecessor.OwnerID || predecessor.FencingEpoch != in.Predecessor.FencingEpoch {
		return BuildAttemptSuccessorResult{}, ErrStaleFence
	}
	if predecessor.State == AttemptCommitted || predecessor.State == AttemptAborted || predecessor.State == AttemptFenced {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: predecessor attempt is %s", ErrConflict, predecessor.State)
	}
	if predecessor.CandidateID == "" || predecessor.PlanID == "" || predecessor.PhysicalPoolID == "" {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: predecessor attempt identity is incomplete", ErrConflict)
	}
	// The predecessor must already be indeterminate. Marker absence is not
	// process-termination evidence and therefore must never overwrite the
	// predecessor's durable termination_evidence document.
	if predecessor.State != AttemptIndeterminate || len(predecessor.TerminationEvidence) == 0 {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: predecessor must already be indeterminate", ErrConflict)
	}
	catalogID, err := textID(in.CatalogID, "catalog id")
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	resolution, err := normalizeSuccessorResolutionEvidence(in.ResolutionEvidence, predecessor, catalogID)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if err := r.ReleaseLeaseAfterAttemptTerminationTx(ctx, tx, in.Predecessor); err != nil {
		return BuildAttemptSuccessorResult{}, err
	}

	// Successor IDs are explicit so a retried transaction can replay exactly;
	// silently generating a new UUID on each retry would violate idempotency.
	successorID, err := uuidID(in.SuccessorAttempt.AttemptID, "successor attempt id", false)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if successorID == predecessor.AttemptID {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor must use a new attempt UUID", ErrConflict)
	}
	if in.SuccessorAttempt.Namespace != "" || in.SuccessorAttempt.FencingEpoch != 0 {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor namespace and fencing epoch are authority-derived", ErrInvalid)
	}
	if in.SuccessorAttempt.OwnerID == "" || in.SuccessorAttempt.OwnerID != strings.TrimSpace(in.SuccessorAttempt.OwnerID) || in.SuccessorAttempt.SessionIdentity == "" || in.SuccessorAttempt.SessionIdentity != strings.TrimSpace(in.SuccessorAttempt.SessionIdentity) {
		return BuildAttemptSuccessorResult{}, ErrInvalid
	}
	if in.SuccessorAttempt.SessionIdentity == predecessor.SessionIdentity {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor must use a new session identity", ErrConflict)
	}
	if in.SuccessorAttempt.PlanID != predecessor.PlanID || in.SuccessorAttempt.CandidateID != predecessor.CandidateID || in.SuccessorAttempt.PhysicalPoolID != predecessor.PhysicalPoolID || in.SuccessorAttempt.CatalogID != catalogID || predecessor.CatalogID != catalogID || in.SuccessorAttempt.RequestDigest != predecessor.RequestDigest || in.SuccessorAttempt.PlanDigest != predecessor.PlanDigest {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor attempt identity differs from predecessor", ErrConflict)
	}
	if in.SuccessorLease.OwnerID != in.SuccessorAttempt.OwnerID || in.SuccessorLease.TargetID != in.Predecessor.TargetID {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor lease and attempt owners or target differ", ErrConflict)
	}
	if in.SuccessorLease.LeaseID == "" || in.SuccessorLease.LeaseID == in.Predecessor.LeaseID {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor must use a new lease identity", ErrConflict)
	}
	if in.SuccessorLease.ExpiresAt.IsZero() {
		return BuildAttemptSuccessorResult{}, ErrInvalid
	}

	successorLease, err := acquireLease(ctx, tx, in.SuccessorLease)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if successorLease.FencingEpoch <= predecessor.FencingEpoch {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor fencing epoch did not advance", ErrConflict)
	}
	successorInput := in.SuccessorAttempt
	successorInput.AttemptID = successorID
	successorInput.FencingEpoch = successorLease.FencingEpoch
	successorInput.LeaseExpiresAt = successorLease.ExpiresAt
	successorInput.Namespace, err = deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{CandidateID: successorInput.CandidateID, AttemptID: successorID, FencingEpoch: successorLease.FencingEpoch})
	if err != nil {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: derive successor relation namespace: %v", ErrInvalid, err)
	}
	if successorInput.Namespace == predecessor.Namespace {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor relation namespace reused predecessor", ErrConflict)
	}
	successor, err := beginBuildAttempt(ctx, tx, successorInput)
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if successor.AttemptID != successorID || successor.FencingEpoch != successorLease.FencingEpoch || successor.Namespace != successorInput.Namespace || successor.SessionIdentity == predecessor.SessionIdentity {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor attempt identity drifted", ErrConflict)
	}
	if err := depdb.New(tx).InsertBuildAttemptSuccessor(ctx, depdb.InsertBuildAttemptSuccessorParams{PredecessorAttemptID: dbUUID(predecessor.AttemptID), SuccessorAttemptID: dbUUID(successor.AttemptID), ResolutionEvidence: resolution}); err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	link, err := depdb.New(tx).GetBuildAttemptSuccessor(ctx, dbUUID(predecessor.AttemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return BuildAttemptSuccessorResult{}, ErrConflict
	}
	if err != nil {
		return BuildAttemptSuccessorResult{}, err
	}
	if link.SuccessorAttemptID != successor.AttemptID || !sameCanonical(link.ResolutionEvidence, resolution) {
		return BuildAttemptSuccessorResult{}, fmt.Errorf("%w: successor link identity differs", ErrConflict)
	}
	return BuildAttemptSuccessorResult{Predecessor: predecessor, SuccessorLease: successorLease, Successor: successor, ResolutionEvidence: append(json.RawMessage(nil), resolution...)}, nil
}

// BuildAttemptSuccessorTx returns the immutable successor edge, if one was
// admitted for predecessorAttemptID.
func (r *Repository) BuildAttemptSuccessorTx(ctx context.Context, tx Tx, predecessorAttemptID string) (BuildAttemptSuccessorLink, error) {
	if tx == nil {
		return BuildAttemptSuccessorLink{}, ErrInvalid
	}
	id, err := uuidID(predecessorAttemptID, "predecessor attempt id", false)
	if err != nil {
		return BuildAttemptSuccessorLink{}, err
	}
	row, err := depdb.New(tx).GetBuildAttemptSuccessor(contextOrBackground(ctx), dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return BuildAttemptSuccessorLink{}, ErrNotFound
	}
	if err != nil {
		return BuildAttemptSuccessorLink{}, err
	}
	return BuildAttemptSuccessorLink{PredecessorAttemptID: row.PredecessorAttemptID, SuccessorAttemptID: row.SuccessorAttemptID, ResolutionEvidence: append(json.RawMessage(nil), row.ResolutionEvidence...), CreatedAt: dbTime(row.CreatedAt)}, nil
}

// AcquireLeaseAndBeginBuildAttemptTx atomically acquires a target writer
// lease and records the corresponding build attempt through a caller-owned
// PostgreSQL transaction.  The attempt inherits the durable lease fencing
// epoch and expiry when those fields are left zero, while explicitly supplied
// values must match the acquired lease.  The caller owns the final
// commit/rollback, so a failed attempt admission can roll back the lease as
// well.
func (r *Repository) AcquireLeaseAndBeginBuildAttemptTx(
	ctx context.Context,
	tx Tx,
	leaseInput LeaseInput,
	attemptInput BuildAttemptInput,
) (DeliveryLease, DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryLease{}, DeliveryBuildAttempt{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	if attemptInput.OwnerID != leaseInput.OwnerID {
		return DeliveryLease{}, DeliveryBuildAttempt{}, fmt.Errorf("%w: lease and build attempt owners differ", ErrConflict)
	}
	lease, err := acquireLease(ctx, tx, leaseInput)
	if err != nil {
		return DeliveryLease{}, DeliveryBuildAttempt{}, err
	}
	if attemptInput.FencingEpoch == 0 {
		attemptInput.FencingEpoch = lease.FencingEpoch
	} else if attemptInput.FencingEpoch != lease.FencingEpoch {
		return DeliveryLease{}, DeliveryBuildAttempt{}, fmt.Errorf("%w: build attempt fencing epoch differs from lease", ErrConflict)
	}
	if attemptInput.LeaseExpiresAt.IsZero() {
		attemptInput.LeaseExpiresAt = lease.ExpiresAt
	} else if !attemptInput.LeaseExpiresAt.UTC().Truncate(time.Microsecond).Equal(lease.ExpiresAt.UTC().Truncate(time.Microsecond)) {
		return DeliveryLease{}, DeliveryBuildAttempt{}, fmt.Errorf("%w: build attempt lease expiry differs from lease", ErrConflict)
	}
	attempt, err := beginBuildAttempt(ctx, tx, attemptInput)
	if err != nil {
		return DeliveryLease{}, DeliveryBuildAttempt{}, err
	}
	return lease, attempt, nil
}

func beginBuildAttempt(ctx context.Context, db DBTX, in BuildAttemptInput) (DeliveryBuildAttempt, error) {
	id, err := uuidID(in.AttemptID, "attempt id", true)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	plan, err := uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	candidate := in.CandidateID
	if candidate != "" {
		if candidate, err = uuidID(candidate, "candidate id", false); err != nil {
			return DeliveryBuildAttempt{}, err
		}
	}
	owner, err := textID(in.OwnerID, "owner id")
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if in.FencingEpoch <= 0 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	for n, v := range map[string]string{"request digest": in.RequestDigest, "plan digest": in.PlanDigest} {
		if _, err := digest(v, n); err != nil {
			return DeliveryBuildAttempt{}, err
		}
	}
	if in.PhysicalPoolID == "" || in.PhysicalPoolID != strings.TrimSpace(in.PhysicalPoolID) || len(in.PhysicalPoolID) > 255 || in.CatalogID == "" || in.CatalogID != strings.TrimSpace(in.CatalogID) || len(in.CatalogID) > 255 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	if in.Namespace == "" || in.Namespace != strings.TrimSpace(in.Namespace) || len(in.Namespace) > 512 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	if in.SessionIdentity == "" || in.SessionIdentity != strings.TrimSpace(in.SessionIdentity) || len(in.SessionIdentity) > 512 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	lease := in.LeaseExpiresAt.UTC().Truncate(time.Microsecond)
	now, err := databaseNow(ctx, db)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if lease.IsZero() || !lease.After(now) || lease.After(now.Add(maxLease)) {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	if candidate != "" {
		var candidatePlan string
		candidatePlan, err = depdb.New(db).GetCandidatePlan(ctx, dbUUID(candidate))
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryBuildAttempt{}, ErrNotFound
		} else if err != nil {
			return DeliveryBuildAttempt{}, err
		} else if candidatePlan != plan {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	}
	err = depdb.New(db).InsertBuildAttempt(ctx, depdb.InsertBuildAttemptParams{AttemptID: dbUUID(id), PlanID: dbUUID(plan), CandidateID: dbUUID(candidate), OwnerID: owner, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, FencingEpoch: in.FencingEpoch, RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, Namespace: in.Namespace, LeaseExpiresAt: pgTime(lease), SessionIdentity: in.SessionIdentity})
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	a, err := loadAttempt(ctx, db, id)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if a.PlanID != plan || a.CandidateID != candidate || a.OwnerID != owner || a.PhysicalPoolID != in.PhysicalPoolID || a.CatalogID != in.CatalogID || a.FencingEpoch != in.FencingEpoch || a.RequestDigest != in.RequestDigest || a.PlanDigest != in.PlanDigest || a.Namespace != in.Namespace || a.SessionIdentity != in.SessionIdentity || !a.LeaseExpiresAt.Equal(lease) {
		return DeliveryBuildAttempt{}, ErrConflict
	}
	return a, nil
}
func loadAttempt(ctx context.Context, db DBTX, id string) (DeliveryBuildAttempt, error) {
	var a DeliveryBuildAttempt
	row, err := depdb.New(db).GetBuildAttempt(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryBuildAttempt{}, ErrNotFound
	}
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	a.AttemptID, a.PlanID, a.CandidateID, a.OwnerID, a.PhysicalPoolID, a.CatalogID, a.FencingEpoch, a.RequestDigest, a.PlanDigest, a.State, a.Namespace, a.LeaseExpiresAt, a.SessionIdentity, a.SnapshotID, a.CreatedAt, a.UpdatedAt = row.AttemptID, row.PlanID, row.CandidateID, row.OwnerID, row.PhysicalPoolID, row.CatalogID, row.FencingEpoch, row.RequestDigest, row.PlanDigest, BuildAttemptState(row.State), row.Namespace, dbTime(row.LeaseExpiresAt), row.SessionIdentity, row.SnapshotID, dbTime(row.CreatedAt), dbTime(row.UpdatedAt)
	a.CommitMarker, a.TerminationEvidence = append([]byte(nil), row.CommitMarker...), append([]byte(nil), row.TerminationEvidence...)
	if row.FinishedAt.Valid {
		a.FinishedAt = row.FinishedAt.Time.UTC()
	}
	return a, nil
}
func (r *Repository) BuildAttempt(ctx context.Context, id string) (DeliveryBuildAttempt, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	id, err = uuidID(id, "attempt id", false)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return loadAttempt(contextOrBackground(ctx), db, id)
}

// BuildAttemptTx reads immutable delivery build-attempt evidence through a
// caller-owned transaction.
func (r *Repository) BuildAttemptTx(ctx context.Context, tx Tx, id string) (DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	id, err := uuidID(id, "attempt id", false)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return loadAttempt(contextOrBackground(ctx), tx, id)
}
func (r *Repository) LoadBuildAttempt(ctx context.Context, id string) (DeliveryBuildAttempt, error) {
	return r.BuildAttempt(ctx, id)
}

// BindBuildArtifact records the immutable artifact produced by an
// attempt. It owns a short transaction so the lock, lease check, insert, and
// replay read share one atomic boundary.
func (r *Repository) BindBuildArtifact(ctx context.Context, in BuildArtifactBindingInput) (BuildArtifactBinding, error) {
	tx, err := r.begin(contextOrBackground(ctx))
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	out, err := r.BindBuildArtifactTx(ctx, tx, in)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return BuildArtifactBinding{}, err
	}
	committed = true
	return out, nil
}

// BindBuildArtifactTx records (or exactly replays) a binding using a
// caller-owned transaction. The transaction is never committed or rolled
// back by this method.
func (r *Repository) BindBuildArtifactTx(ctx context.Context, tx Tx, in BuildArtifactBindingInput) (BuildArtifactBinding, error) {
	if tx == nil {
		return BuildArtifactBinding{}, ErrInvalid
	}
	return bindBuildArtifact(contextOrBackground(ctx), tx, in)
}

// BindRecoveredBuildArtifactTx records (or exactly replays) a recovered
// artifact binding through a caller-owned transaction. Recovery may bind only
// an already-indeterminate attempt and does not require its lease to remain
// active; a running writer must use the ordinary active-lease path. The
// transaction is never committed or rolled back by this method.
func (r *Repository) BindRecoveredBuildArtifactTx(ctx context.Context, tx Tx, in RecoveredBuildArtifactBindingInput) (BuildArtifactBinding, error) {
	if tx == nil {
		return BuildArtifactBinding{}, ErrInvalid
	}
	return bindRecoveredBuildArtifact(contextOrBackground(ctx), tx, in)
}

func canonicalBuildArtifactBindingInput(in BuildArtifactBindingInput) (attempt, artifactID, artifactDigest, servingState, owner string, fence int64, err error) {
	attempt, err = uuidID(in.AttemptID, "attempt id", false)
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	artifactID, err = textID(in.ServingArtifactID, "serving artifact id")
	if err != nil || !validBuildArtifactIdentity(artifactID) {
		if err == nil {
			err = fmt.Errorf("%w: serving artifact id is invalid", ErrInvalid)
		}
		return "", "", "", "", "", 0, err
	}
	artifactDigest, err = digest(in.ServingArtifactDigest, "serving artifact digest")
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	servingState, err = textID(in.ServingStateID, "serving state id")
	if err != nil || !validBuildArtifactIdentity(servingState) {
		if err == nil {
			err = fmt.Errorf("%w: serving state id is invalid", ErrInvalid)
		}
		return "", "", "", "", "", 0, err
	}
	owner, err = textID(in.OwnerID, "owner id")
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	if in.FencingEpoch <= 0 {
		return "", "", "", "", "", 0, ErrInvalid
	}
	return attempt, artifactID, artifactDigest, servingState, owner, in.FencingEpoch, nil
}

func validBuildArtifactIdentity(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:/-", char) {
			continue
		}
		return false
	}
	return value != ""
}

func loadBuildArtifactBinding(ctx context.Context, db DBTX, attempt string) (BuildArtifactBinding, error) {
	row, err := depdb.New(db).GetBuildArtifactBinding(ctx, dbUUID(attempt))
	if errors.Is(err, pgx.ErrNoRows) {
		return BuildArtifactBinding{}, ErrNotFound
	}
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	return BuildArtifactBinding{
		AttemptID: row.AttemptID, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest,
		ServingStateID: row.ServingStateID, BoundAt: dbTime(row.BoundAt),
	}, nil
}

func bindBuildArtifact(ctx context.Context, db DBTX, in BuildArtifactBindingInput) (BuildArtifactBinding, error) {
	attempt, artifactID, artifactDigest, servingState, owner, fence, err := canonicalBuildArtifactBindingInput(in)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	// Lock the attempt before checking existing binding state. This serializes
	// a first bind against terminal transition and makes replay deterministic.
	if _, lockErr := depdb.New(db).LockBuildAttempt(ctx, dbUUID(attempt)); errors.Is(lockErr, pgx.ErrNoRows) {
		return BuildArtifactBinding{}, ErrNotFound
	} else if lockErr != nil {
		return BuildArtifactBinding{}, lockErr
	}
	at, err := loadAttempt(ctx, db, attempt)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	if at.OwnerID != owner || at.FencingEpoch != fence {
		return BuildArtifactBinding{}, ErrStaleFence
	}

	existing, existingErr := loadBuildArtifactBinding(ctx, db, attempt)
	if existingErr == nil {
		if existing.ServingArtifactID != artifactID || existing.ServingArtifactDigest != artifactDigest || existing.ServingStateID != servingState {
			return BuildArtifactBinding{}, ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return BuildArtifactBinding{}, existingErr
	}
	if at.State != AttemptRunning {
		return BuildArtifactBinding{}, fmt.Errorf("%w: build attempt is terminal and has no artifact binding", ErrConflict)
	}
	leaseActive, err := depdb.New(db).BuildAttemptLeaseActive(ctx, dbUUID(attempt))
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	if !leaseActive {
		return BuildArtifactBinding{}, ErrLeaseExpired
	}
	if err := depdb.New(db).InsertBuildArtifactBinding(ctx, depdb.InsertBuildArtifactBindingParams{
		AttemptID: dbUUID(attempt), ServingArtifactID: artifactID, ServingArtifactDigest: artifactDigest, ServingStateID: servingState,
	}); err != nil {
		return BuildArtifactBinding{}, err
	}
	bound, err := loadBuildArtifactBinding(ctx, db, attempt)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	if bound.ServingArtifactID != artifactID || bound.ServingArtifactDigest != artifactDigest || bound.ServingStateID != servingState {
		return BuildArtifactBinding{}, ErrConflict
	}
	return bound, nil
}

func bindRecoveredBuildArtifact(ctx context.Context, db DBTX, in RecoveredBuildArtifactBindingInput) (BuildArtifactBinding, error) {
	// Reuse the ordinary binding canonicalization for all bounded identities,
	// but deliberately keep the recovery path separate so normal binding's
	// active-lease and running-state rules remain unchanged.
	attempt, artifactID, artifactDigest, servingState, owner, fence, err := canonicalBuildArtifactBindingInput(BuildArtifactBindingInput{
		AttemptID: in.AttemptID, ServingArtifactID: in.ServingArtifactID,
		ServingArtifactDigest: in.ServingArtifactDigest, ServingStateID: in.ServingStateID,
		OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch,
	})
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	marker, canonicalMarker, markerErr := decodeCommitMarker(in.CommitMarker, true)
	if markerErr != nil {
		return BuildArtifactBinding{}, fmt.Errorf("%w: invalid recovery commit marker: %v", ErrInvalid, markerErr)
	}

	// Lock the delivery attempt before reading its state or binding. This is
	// the same delivery-side lock used by ordinary binding and preserves the
	// delivery -> DuckLake lock order for callers that compose this Tx method
	// with physical reconciliation.
	if _, lockErr := depdb.New(db).LockBuildAttempt(ctx, dbUUID(attempt)); errors.Is(lockErr, pgx.ErrNoRows) {
		return BuildArtifactBinding{}, ErrNotFound
	} else if lockErr != nil {
		return BuildArtifactBinding{}, lockErr
	}
	at, err := loadAttempt(ctx, db, attempt)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	if at.OwnerID != owner || at.FencingEpoch != fence {
		return BuildArtifactBinding{}, ErrStaleFence
	}
	if !markerIdentityMatches(marker, attempt, at.PhysicalPoolID, at.RequestDigest, at.PlanDigest, fence) {
		return BuildArtifactBinding{}, fmt.Errorf("%w: recovery commit marker identity mismatch", ErrConflict)
	}
	if marker.GenerationID != servingState {
		return BuildArtifactBinding{}, fmt.Errorf("%w: serving state differs from recovery commit marker generation", ErrConflict)
	}
	if at.State == AttemptCommitted {
		_, storedCanonicalMarker, storedMarkerErr := decodeCommitMarker(at.CommitMarker, false)
		if storedMarkerErr != nil || !bytes.Equal(storedCanonicalMarker, canonicalMarker) {
			return BuildArtifactBinding{}, fmt.Errorf("%w: committed attempt marker differs from recovery marker", ErrConflict)
		}
	}

	existing, existingErr := loadBuildArtifactBinding(ctx, db, attempt)
	if existingErr == nil {
		if at.State != AttemptIndeterminate && at.State != AttemptCommitted {
			return BuildArtifactBinding{}, fmt.Errorf("%w: build attempt is not recoverable", ErrConflict)
		}
		if existing.ServingArtifactID != artifactID || existing.ServingArtifactDigest != artifactDigest || existing.ServingStateID != servingState {
			return BuildArtifactBinding{}, ErrConflict
		}
		// Exact replay is limited to the indeterminate recovery state and the
		// committed state produced by an earlier successful recovery transaction.
		return existing, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return BuildArtifactBinding{}, existingErr
	}
	if at.State != AttemptIndeterminate {
		return BuildArtifactBinding{}, fmt.Errorf("%w: build attempt is not indeterminate and has no artifact binding", ErrConflict)
	}

	// Recovery evidence, rather than an active delivery lease, authorizes this
	// first bind. In particular, an indeterminate attempt may be recovered
	// after its lease has expired.
	if err := depdb.New(db).InsertBuildArtifactBinding(ctx, depdb.InsertBuildArtifactBindingParams{
		AttemptID: dbUUID(attempt), ServingArtifactID: artifactID, ServingArtifactDigest: artifactDigest, ServingStateID: servingState,
	}); err != nil {
		return BuildArtifactBinding{}, err
	}
	bound, err := loadBuildArtifactBinding(ctx, db, attempt)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	if bound.ServingArtifactID != artifactID || bound.ServingArtifactDigest != artifactDigest || bound.ServingStateID != servingState {
		return BuildArtifactBinding{}, ErrConflict
	}
	return bound, nil
}

// BuildArtifactBinding reads the immutable binding through the repository's
// standalone database handle.
func (r *Repository) BuildArtifactBinding(ctx context.Context, attemptID string) (BuildArtifactBinding, error) {
	db, err := requireDB(r)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	attemptID, err = uuidID(attemptID, "attempt id", false)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	return loadBuildArtifactBinding(contextOrBackground(ctx), db, attemptID)
}

func (r *Repository) BuildArtifactBindingTx(ctx context.Context, tx Tx, attemptID string) (BuildArtifactBinding, error) {
	if tx == nil {
		return BuildArtifactBinding{}, ErrInvalid
	}
	attemptID, err := uuidID(attemptID, "attempt id", false)
	if err != nil {
		return BuildArtifactBinding{}, err
	}
	return loadBuildArtifactBinding(contextOrBackground(ctx), tx, attemptID)
}

func (r *Repository) LoadBuildArtifactBinding(ctx context.Context, attemptID string) (BuildArtifactBinding, error) {
	return r.BuildArtifactBinding(ctx, attemptID)
}

func (r *Repository) CommitBuildAttempt(ctx context.Context, in CommitAttemptInput) (DeliveryBuildAttempt, error) {
	_, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return r.transitionAttempt(contextOrBackground(ctx), in, AttemptCommitted)
}

// CommitBuildAttemptTx commits a build attempt through a caller-owned
// PostgreSQL transaction. The caller retains ownership of tx and must commit
// or roll it back after composing any related control-plane mutations.
func (r *Repository) CommitBuildAttemptTx(ctx context.Context, tx Tx, in CommitAttemptInput) (DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	return transitionAttemptTx(contextOrBackground(ctx), tx, in, AttemptCommitted)
}

func (r *Repository) AbortBuildAttempt(ctx context.Context, in TerminateAttemptInput) (DeliveryBuildAttempt, error) {
	_, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return r.transitionAttempt(contextOrBackground(ctx), CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, CommitMarker: in.Evidence}, AttemptAborted)
}

// AbortBuildAttemptTx aborts a build attempt through a caller-owned
// PostgreSQL transaction. It does not commit or roll back tx.
func (r *Repository) AbortBuildAttemptTx(ctx context.Context, tx Tx, in TerminateAttemptInput) (DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	return transitionAttemptTx(contextOrBackground(ctx), tx, CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, CommitMarker: in.Evidence}, AttemptAborted)
}

func (r *Repository) MarkAttemptIndeterminate(ctx context.Context, in TerminateAttemptInput) (DeliveryBuildAttempt, error) {
	_, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return r.transitionAttempt(contextOrBackground(ctx), CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, CommitMarker: in.Evidence}, AttemptIndeterminate)
}

// MarkAttemptIndeterminateTx marks a build attempt indeterminate through a
// caller-owned PostgreSQL transaction. It does not commit or roll back tx.
func (r *Repository) MarkAttemptIndeterminateTx(ctx context.Context, tx Tx, in TerminateAttemptInput) (DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	return transitionAttemptTx(contextOrBackground(ctx), tx, CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, CommitMarker: in.Evidence}, AttemptIndeterminate)
}

// ReconcileBuildAttempt applies an explicit exact-marker or positive
// session-termination recovery decision. It owns a short transaction; use
// ReconcileBuildAttemptTx when composing the decision with another ledger.
func (r *Repository) ReconcileBuildAttempt(ctx context.Context, in ReconcileBuildAttemptInput) (DeliveryBuildAttempt, error) {
	if _, err := requireDB(r); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	tx, err := r.begin(contextOrBackground(ctx))
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	attempt, err := r.ReconcileBuildAttemptTx(ctx, tx, in)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	committed = true
	return attempt, nil
}

// ReconcileBuildAttemptTx applies exact recovery evidence in a caller-owned
// native PostgreSQL transaction. It never begins, commits, or rolls back tx.
func (r *Repository) ReconcileBuildAttemptTx(ctx context.Context, tx Tx, in ReconcileBuildAttemptInput) (DeliveryBuildAttempt, error) {
	if r == nil || tx == nil || !r.Configured() {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	return reconcileBuildAttemptTx(contextOrBackground(ctx), tx, in)
}

func reconcileBuildAttemptTx(ctx context.Context, db DBTX, in ReconcileBuildAttemptInput) (DeliveryBuildAttempt, error) {
	if db == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	id, err := uuidID(in.AttemptID, "attempt id", false)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	owner, err := textID(in.OwnerID, "owner id")
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if in.FencingEpoch <= 0 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	state := in.State
	if state != AttemptCommitted && state != AttemptAborted {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: reconciliation outcome must be committed or aborted", ErrInvalid)
	}
	var markerCanonical []byte
	if state == AttemptCommitted {
		if in.SnapshotID <= 0 {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		marker, canonical, markerErr := decodeCommitMarker(in.CommitMarker, true)
		if markerErr != nil {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: invalid recovery commit marker: %v", ErrInvalid, markerErr)
		}
		// Identity fields that depend on the persisted attempt are checked after
		// locking it below. This preflight only guarantees marker structure.
		_ = marker
		markerCanonical = canonical
		if len(in.TerminationEvidence) != 0 || in.SessionTerminated {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: committed recovery cannot carry termination evidence", ErrInvalid)
		}
	} else {
		if len(in.CommitMarker) != 0 {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: aborted recovery cannot carry a commit marker", ErrInvalid)
		}
		if !in.SessionTerminated {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: aborted recovery requires positive session-termination evidence", ErrInvalid)
		}
		if in.SessionIdentity == "" || in.SessionIdentity != strings.TrimSpace(in.SessionIdentity) || len(in.SessionIdentity) > 512 || strings.ContainsAny(in.SessionIdentity, "\x00\r\n") {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: session identity is invalid", ErrInvalid)
		}
		if err := validateSessionTerminationEvidence(in.TerminationEvidence, id, owner, in.SessionIdentity, in.FencingEpoch); err != nil {
			return DeliveryBuildAttempt{}, err
		}
	}

	if _, err := depdb.New(db).LockBuildAttempt(ctx, dbUUID(id)); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryBuildAttempt{}, ErrNotFound
	} else if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	at, err := loadAttempt(ctx, db, id)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if at.OwnerID != owner || at.FencingEpoch != in.FencingEpoch {
		return DeliveryBuildAttempt{}, ErrStaleFence
	}
	if state == AttemptAborted && at.SessionIdentity != in.SessionIdentity {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: session-termination evidence session differs", ErrConflict)
	}
	if state == AttemptCommitted {
		marker, _, markerErr := decodeCommitMarker(markerCanonical, false)
		if markerErr != nil || !markerIdentityMatches(marker, id, at.PhysicalPoolID, at.RequestDigest, at.PlanDigest, in.FencingEpoch) {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: recovery commit marker identity mismatch", ErrConflict)
		}
		if at.State == AttemptCommitted {
			stored, _, storedErr := decodeCommitMarker(at.CommitMarker, false)
			if storedErr == nil && at.SnapshotID == in.SnapshotID && markerIdentityMatches(stored, id, at.PhysicalPoolID, at.RequestDigest, at.PlanDigest, in.FencingEpoch) {
				storedCanonical, canonicalErr := stored.CanonicalJSON()
				if canonicalErr == nil && bytes.Equal([]byte(storedCanonical), markerCanonical) {
					return at, nil
				}
			}
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: committed recovery evidence differs", ErrConflict)
		}
	} else if at.State == AttemptAborted {
		evidence, evidenceErr := canonicalNonEmpty(in.TerminationEvidence, maxEvidence)
		if evidenceErr == nil && sameCanonical(at.TerminationEvidence, evidence) {
			return at, nil
		}
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: aborted recovery evidence differs", ErrConflict)
	}
	if at.State != AttemptRunning && at.State != AttemptIndeterminate {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: attempt is %s", ErrConflict, at.State)
	}
	if state == AttemptCommitted {
		rows, err := depdb.New(db).ReconcileBuildAttemptCommitted(ctx, depdb.ReconcileBuildAttemptCommittedParams{AttemptID: dbUUID(id), SnapshotID: pgInt8(&in.SnapshotID), CommitMarker: markerCanonical, OwnerID: owner, FencingEpoch: in.FencingEpoch})
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if rows != 1 {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	} else {
		evidence, evidenceErr := canonicalNonEmpty(in.TerminationEvidence, maxEvidence)
		if evidenceErr != nil {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		rows, err := depdb.New(db).ReconcileBuildAttemptTerminal(ctx, depdb.ReconcileBuildAttemptTerminalParams{AttemptID: dbUUID(id), State: string(state), Evidence: evidence, OwnerID: owner, FencingEpoch: in.FencingEpoch})
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if rows != 1 {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	}
	got, err := loadAttempt(ctx, db, id)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if got.State != state {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: attempt is %s", ErrConflict, got.State)
	}
	if state == AttemptCommitted {
		stored, _, storedErr := decodeCommitMarker(got.CommitMarker, false)
		storedCanonical, canonicalErr := stored.CanonicalJSON()
		if got.SnapshotID != in.SnapshotID || !markerMatches(got.CommitMarker, id, got.PhysicalPoolID, got.RequestDigest, got.PlanDigest, in.FencingEpoch) || storedErr != nil || canonicalErr != nil || !bytes.Equal([]byte(storedCanonical), markerCanonical) {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: committed recovery evidence differs", ErrConflict)
		}
	} else if !sameCanonical(got.TerminationEvidence, in.TerminationEvidence) {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: aborted recovery evidence differs", ErrConflict)
	}
	return got, nil
}

// validateSessionTerminationEvidence requires one closed, attempt-bound,
// positive session-termination document.
type sessionTerminationEvidenceDocument struct {
	SchemaVersion     int    `json:"schema_version"`
	AttemptID         string `json:"attempt_id"`
	OwnerID           string `json:"owner_id"`
	FencingEpoch      int64  `json:"fencing_epoch"`
	SessionIdentity   string `json:"session_identity"`
	SessionTerminated bool   `json:"session_terminated"`
}

func validateSessionTerminationEvidence(raw json.RawMessage, attemptID, ownerID, sessionIdentity string, fencingEpoch int64) error {
	canonical, err := canonicalNonEmpty(raw, maxEvidence)
	if err != nil {
		return fmt.Errorf("%w: positive session-termination evidence is required", ErrInvalid)
	}
	var document sessionTerminationEvidenceDocument
	if err := strictjson.Decode(canonical, &document); err != nil {
		return fmt.Errorf("%w: positive session-termination evidence is invalid", ErrInvalid)
	}
	if document.SchemaVersion != 1 || document.AttemptID != attemptID || document.OwnerID != ownerID || document.FencingEpoch != fencingEpoch || document.SessionIdentity != sessionIdentity || !document.SessionTerminated {
		return fmt.Errorf("%w: positive session-termination evidence identity differs", ErrInvalid)
	}
	return nil
}

// RenewBuildAttemptLeaseTx extends a running build attempt lease on a
// caller-owned transaction. The attempt identity is immutable; only its
// expiry and updated timestamp move forward. This method deliberately shares
// the transaction with the target lease and operation lease during a
// native-build heartbeat.
func (r *Repository) RenewBuildAttemptLeaseTx(ctx context.Context, tx Tx, attemptID, ownerID string, fencingEpoch int64, expiresAt time.Time) (DeliveryBuildAttempt, error) {
	if tx == nil {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	id, err := uuidID(attemptID, "attempt id", false)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	owner, err := textID(ownerID, "owner id")
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if fencingEpoch <= 0 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	exp := expiresAt.UTC().Truncate(time.Microsecond)
	if !exp.After(now) || exp.After(now.Add(maxLease)) {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	command, err := depdb.New(tx).RenewBuildAttemptLease(ctx, depdb.RenewBuildAttemptLeaseParams{AttemptID: dbUUID(id), OwnerID: owner, FencingEpoch: fencingEpoch, ExpiresAt: pgTime(exp)})
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if command.RowsAffected() != 1 {
		current, loadErr := loadAttempt(ctx, tx, id)
		if loadErr != nil {
			return DeliveryBuildAttempt{}, loadErr
		}
		if current.OwnerID != owner || current.FencingEpoch != fencingEpoch {
			return DeliveryBuildAttempt{}, ErrStaleFence
		}
		if current.State != AttemptRunning {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: build attempt is %s", ErrConflict, current.State)
		}
		if current.LeaseExpiresAt.After(exp) {
			return DeliveryBuildAttempt{}, ErrConflict
		}
		return DeliveryBuildAttempt{}, ErrLeaseExpired
	}
	return loadAttempt(ctx, tx, id)
}

func (r *Repository) transitionAttempt(ctx context.Context, in CommitAttemptInput, state BuildAttemptState) (DeliveryBuildAttempt, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	a, err := transitionAttemptTx(ctx, tx, in, state)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	committed = true
	return a, nil
}
func transitionAttemptTx(ctx context.Context, db DBTX, in CommitAttemptInput, state BuildAttemptState) (DeliveryBuildAttempt, error) {
	if state != AttemptCommitted && state != AttemptAborted && state != AttemptIndeterminate {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	id, err := uuidID(in.AttemptID, "attempt id", false)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	owner, err := textID(in.OwnerID, "owner id")
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if in.FencingEpoch <= 0 {
		return DeliveryBuildAttempt{}, ErrInvalid
	}
	// Lock the row through the whole read/validate/update sequence. Without
	// this, two workers can both observe running and report the other's winner
	// as their own successful transition.
	if _, err := depdb.New(db).LockBuildAttempt(ctx, dbUUID(id)); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryBuildAttempt{}, ErrNotFound
	} else if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	at, err := loadAttempt(ctx, db, id)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if at.OwnerID != owner || at.FencingEpoch != in.FencingEpoch {
		return DeliveryBuildAttempt{}, ErrStaleFence
	}
	// Commit markers are durable DuckLake identity evidence. Validate the
	// complete marker schema even on idempotent retries; accepting a sparse or
	// unknown-field marker here would make the retry path weaker than the
	// initial commit path.
	var inputMarker catalogartifact.CommitMarker
	var inputMarkerCanonical []byte
	if state == AttemptCommitted {
		inputMarker, inputMarkerCanonical, err = decodeCommitMarker(in.CommitMarker, true)
		if err != nil {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: invalid commit marker: %v", ErrInvalid, err)
		}
		if !markerIdentityMatches(inputMarker, id, at.PhysicalPoolID, at.RequestDigest, at.PlanDigest, at.FencingEpoch) {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: commit marker identity mismatch", ErrConflict)
		}
	}
	if at.State != AttemptRunning {
		if state == AttemptCommitted {
			storedMarker, _, storedErr := decodeCommitMarker(at.CommitMarker, false)
			if storedErr == nil && markerIdentityMatches(storedMarker, id, at.PhysicalPoolID, at.RequestDigest, at.PlanDigest, at.FencingEpoch) && at.State == state && at.SnapshotID == in.SnapshotID {
				storedCanonical, canonicalErr := storedMarker.CanonicalJSON()
				if canonicalErr == nil && bytes.Equal([]byte(storedCanonical), inputMarkerCanonical) {
					return at, nil
				}
			}
		} else {
			canonicalEvidence, evidenceErr := canonicalNonEmpty(in.CommitMarker, 32768)
			if evidenceErr == nil && at.State == state && sameCanonical(at.TerminationEvidence, canonicalEvidence) {
				return at, nil
			}
		}
		return DeliveryBuildAttempt{}, ErrConflict
	}
	// A lease timeout is not termination evidence, but once the caller has
	// supplied positive no-commit/session-terminated evidence it must still be
	// possible to close the durable attempt. Only a commit requires an active
	// lease; abort and indeterminate transitions remain fenced by owner/epoch.
	if state == AttemptCommitted {
		leaseActive, err := depdb.New(db).BuildAttemptLeaseActive(ctx, dbUUID(id))
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if !leaseActive {
			return DeliveryBuildAttempt{}, ErrLeaseExpired
		}
		if in.SnapshotID <= 0 {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		rows, err := depdb.New(db).CommitBuildAttempt(ctx, depdb.CommitBuildAttemptParams{AttemptID: dbUUID(id), SnapshotID: pgInt8(&in.SnapshotID), CommitMarker: inputMarkerCanonical, OwnerID: owner, FencingEpoch: in.FencingEpoch})
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if rows != 1 {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	} else {
		evidence, err := canonicalNonEmpty(in.CommitMarker, 32768)
		if err != nil {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		rows, err := depdb.New(db).TerminateBuildAttempt(ctx, depdb.TerminateBuildAttemptParams{AttemptID: dbUUID(id), State: string(state), Evidence: evidence, OwnerID: owner, FencingEpoch: in.FencingEpoch})
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if rows != 1 {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	}
	return loadAttempt(ctx, db, id)
}

func markerMatches(raw []byte, attempt, physicalPool, request, plan string, fence int64) bool {
	m, _, err := decodeCommitMarker(raw, false)
	return err == nil && markerIdentityMatches(m, attempt, physicalPool, request, plan, fence)
}

func markerIdentityMatches(m catalogartifact.CommitMarker, attempt, physicalPool, request, plan string, fence int64) bool {
	return m.AttemptID == attempt && m.PhysicalPoolID == physicalPool && m.RequestDigest == request && m.PlanDigest == plan && m.LeaseEpoch == fence
}

// decodeCommitMarker performs the full DuckLake marker decode and Normalize
// validation. PostgreSQL jsonb does not preserve input key order, so callers
// validating a stored marker must set requireCanonical to false and compare
// the normalized value semantically. The initial commit input is required to
// use DuckLake's canonical byte ordering because that exact string is written
// to commit_extra_info by the DuckLake writer.
func decodeCommitMarker(raw []byte, requireCanonical bool) (catalogartifact.CommitMarker, []byte, error) {
	if len(raw) == 0 {
		return catalogartifact.CommitMarker{}, nil, errors.New("commit marker is empty")
	}
	if len(raw) > catalogartifact.MaxCommitMarkerBytes {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("commit marker exceeds %d bytes", catalogartifact.MaxCommitMarkerBytes)
	}
	normalized, err := catalogartifact.DecodeCommitMarker(raw)
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, err
	}
	canonical, err := normalized.CanonicalJSON()
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, err
	}
	canonicalBytes := []byte(canonical)
	if requireCanonical && !bytes.Equal(raw, canonicalBytes) {
		return catalogartifact.CommitMarker{}, nil, errors.New("commit marker is not canonical JSON")
	}
	return normalized, canonicalBytes, nil
}

// CreateSnapshotSeal accepts only a committed attempt and exact marker
// identity.  It never reads catalog state or copies catalog metadata.
func (r *Repository) CreateSnapshotSeal(ctx context.Context, in SnapshotSealInput) (SnapshotSeal, error) {
	db, err := requireDB(r)
	if err != nil {
		return SnapshotSeal{}, err
	}
	return createSeal(contextOrBackground(ctx), db, in)
}

// CreateSnapshotSealTx creates (or exactly replays) a snapshot seal through a
// caller-owned PostgreSQL transaction. It deliberately does not commit or
// roll back tx so callers can compose seal creation with adjacent mutations.
func (r *Repository) CreateSnapshotSealTx(ctx context.Context, tx Tx, in SnapshotSealInput) (SnapshotSeal, error) {
	if tx == nil {
		return SnapshotSeal{}, ErrInvalid
	}
	return createSeal(contextOrBackground(ctx), tx, in)
}

func createSeal(ctx context.Context, db DBTX, in SnapshotSealInput) (SnapshotSeal, error) {
	id, err := uuidID(in.SealID, "seal id", true)
	if err != nil {
		return SnapshotSeal{}, err
	}
	attempt, err := uuidID(in.AttemptID, "attempt id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	candidate, err := uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	if in.PhysicalPoolID == "" || in.TenantDomain == "" || in.Region == "" || in.EncryptionDomain == "" || in.ObjectNamespace == "" || in.CatalogDatabase == "" || in.CatalogID == "" || in.CatalogUUID == "" || in.RelationNamespace == "" || in.ObjectRoot == "" || in.ArtifactRoot == "" || in.ObjectRootDigest == "" || in.ArtifactRootDigest == "" {
		return SnapshotSeal{}, ErrInvalid
	}
	canonicalCatalogUUID, err := uuidID(in.CatalogUUID, "catalog uuid", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	in.CatalogUUID = canonicalCatalogUUID
	for label, value := range map[string]string{"physical pool id": in.PhysicalPoolID, "catalog database": in.CatalogDatabase, "catalog id": in.CatalogID, "relation namespace": in.RelationNamespace, "object root": in.ObjectRoot, "artifact root": in.ArtifactRoot} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return SnapshotSeal{}, fmt.Errorf("%w: %s", ErrInvalid, label)
		}
	}
	for label, value := range map[string]string{"tenant domain": in.TenantDomain, "region": in.Region, "encryption domain": in.EncryptionDomain, "object namespace": in.ObjectNamespace} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
			return SnapshotSeal{}, fmt.Errorf("%w: %s", ErrInvalid, label)
		}
	}
	if _, err := digest(in.ObjectRootDigest, "object root digest"); err != nil {
		return SnapshotSeal{}, err
	}
	if _, err := digest(in.ArtifactRootDigest, "artifact root digest"); err != nil {
		return SnapshotSeal{}, err
	}
	if in.CatalogVersion <= 0 || in.DuckLakeSnapshotID <= 0 {
		return SnapshotSeal{}, ErrInvalid
	}
	for n, v := range map[string]string{"relation manifest digest": in.RelationManifestDigest, "closure digest": in.ClosureDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "request digest": in.RequestDigest, "plan digest": in.PlanDigest, "compatibility digest": in.CompatibilityDigest, "serving artifact digest": in.ServingArtifactDigest} {
		if _, err := digest(v, n); err != nil {
			return SnapshotSeal{}, err
		}
	}
	if in.ServingArtifactID == "" || in.ServingArtifactID != strings.TrimSpace(in.ServingArtifactID) || len(in.ServingArtifactID) > 255 || strings.ContainsAny(in.ServingArtifactID, "\x00\r\n") {
		return SnapshotSeal{}, fmt.Errorf("%w: serving artifact id", ErrInvalid)
	}
	for n, v := range map[string]string{"DuckDB version": in.DuckDBVersion, "runtime version": in.RuntimeVersion, "DuckLake extension version": in.DuckLakeExtensionVersion, "DuckLake specification version": in.DuckLakeSpecVersion, "catalog schema version": in.CatalogSchemaVersion} {
		if v == "" || v != strings.TrimSpace(v) || len(v) > 128 {
			return SnapshotSeal{}, fmt.Errorf("%w: %s", ErrInvalid, n)
		}
	}
	evidence, err := canonicalObject(in.QualificationEvidence, maxEvidence, false)
	if err != nil {
		return SnapshotSeal{}, fmt.Errorf("%w: qualification evidence required", ErrInvalid)
	}
	at, err := loadAttempt(ctx, db, attempt)
	if err != nil {
		return SnapshotSeal{}, err
	}
	if at.State != AttemptCommitted || at.SnapshotID != in.DuckLakeSnapshotID || at.RequestDigest != in.RequestDigest || at.PlanDigest != in.PlanDigest || at.CandidateID != candidate || at.PhysicalPoolID != in.PhysicalPoolID || at.CatalogID != in.CatalogID || at.Namespace != in.RelationNamespace {
		return SnapshotSeal{}, fmt.Errorf("%w: attempt is not exact committed evidence", ErrNotQualified)
	}
	if !markerMatches(at.CommitMarker, attempt, at.PhysicalPoolID, in.RequestDigest, in.PlanDigest, at.FencingEpoch) {
		return SnapshotSeal{}, fmt.Errorf("%w: commit marker is incomplete", ErrNotQualified)
	}
	marker, _, markerErr := decodeCommitMarker(at.CommitMarker, false)
	if markerErr != nil {
		return SnapshotSeal{}, fmt.Errorf("%w: commit marker is incomplete", ErrNotQualified)
	}
	binding, bindingErr := loadBuildArtifactBinding(ctx, db, attempt)
	if errors.Is(bindingErr, ErrNotFound) {
		return SnapshotSeal{}, fmt.Errorf("%w: build artifact binding is missing", ErrNotQualified)
	}
	if bindingErr != nil {
		return SnapshotSeal{}, bindingErr
	}
	if binding.ServingArtifactID != in.ServingArtifactID || binding.ServingArtifactDigest != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: build artifact binding differs", ErrConflict)
	}
	if binding.ServingStateID != marker.GenerationID {
		return SnapshotSeal{}, fmt.Errorf("%w: serving state differs from commit marker generation", ErrConflict)
	}
	ci, err := depdb.New(db).GetCandidateIdentity(ctx, dbUUID(candidate))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	} else if err != nil {
		return SnapshotSeal{}, err
	}
	if ci.Status == "rejected" || ci.Status == "retired" || ci.PlanID != at.PlanID || ci.ArtifactDigest != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: candidate evidence differs", ErrNotQualified)
	}
	pi, err := depdb.New(db).GetPlanDigests(ctx, dbUUID(at.PlanID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	} else if err != nil {
		return SnapshotSeal{}, err
	}
	if pi.PlanDigest != in.PlanDigest || pi.CompiledGraphDigest != in.CompiledGraphDigest || pi.CompiledConfigDigest != in.CompiledConfigDigest || pi.SecurityDomainFingerprint != in.SecurityDomainFingerprint || pi.ArtifactDigest != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: plan evidence differs", ErrNotQualified)
	}
	err = depdb.New(db).InsertSnapshotSeal(ctx, depdb.InsertSnapshotSealParams{SealID: dbUUID(id), AttemptID: dbUUID(attempt), CandidateID: dbUUID(candidate), PhysicalPoolID: in.PhysicalPoolID, TenantDomain: in.TenantDomain, Region: in.Region, EncryptionDomain: in.EncryptionDomain, ObjectNamespace: in.ObjectNamespace, CatalogDatabase: in.CatalogDatabase, CatalogID: in.CatalogID, CatalogUuid: in.CatalogUUID, CatalogVersion: in.CatalogVersion, DucklakeSnapshotID: in.DuckLakeSnapshotID, RelationNamespace: in.RelationNamespace, RelationManifestDigest: in.RelationManifestDigest, ClosureDigest: in.ClosureDigest, ObjectRoot: in.ObjectRoot, ObjectRootDigest: in.ObjectRootDigest, ArtifactRoot: in.ArtifactRoot, ArtifactRootDigest: in.ArtifactRootDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, CompatibilityDigest: in.CompatibilityDigest, ServingArtifactID: in.ServingArtifactID, ServingArtifactDigest: in.ServingArtifactDigest, DuckdbVersion: in.DuckDBVersion, RuntimeVersion: in.RuntimeVersion, DucklakeExtensionVersion: in.DuckLakeExtensionVersion, DucklakeSpecVersion: in.DuckLakeSpecVersion, CatalogSchemaVersion: in.CatalogSchemaVersion, QualificationEvidence: evidence})
	if err != nil {
		return SnapshotSeal{}, err
	}
	s, err := loadSeal(ctx, db, id)
	if err != nil {
		return SnapshotSeal{}, err
	}
	if !sameSealIdentity(s, in) {
		return SnapshotSeal{}, ErrConflict
	}
	return s, nil
}
func sameSealIdentity(a SnapshotSeal, b SnapshotSeal) bool {
	return a.AttemptID == b.AttemptID && a.CandidateID == b.CandidateID && a.PhysicalPoolID == b.PhysicalPoolID && a.TenantDomain == b.TenantDomain && a.Region == b.Region && a.EncryptionDomain == b.EncryptionDomain && a.ObjectNamespace == b.ObjectNamespace && a.CatalogDatabase == b.CatalogDatabase && a.CatalogID == b.CatalogID && a.CatalogUUID == b.CatalogUUID && a.CatalogVersion == b.CatalogVersion && a.DuckLakeSnapshotID == b.DuckLakeSnapshotID && a.RelationNamespace == b.RelationNamespace && a.RelationManifestDigest == b.RelationManifestDigest && a.ClosureDigest == b.ClosureDigest && a.ObjectRoot == b.ObjectRoot && a.ObjectRootDigest == b.ObjectRootDigest && a.ArtifactRoot == b.ArtifactRoot && a.ArtifactRootDigest == b.ArtifactRootDigest && a.CompiledGraphDigest == b.CompiledGraphDigest && a.CompiledConfigDigest == b.CompiledConfigDigest && a.SecurityDomainFingerprint == b.SecurityDomainFingerprint && a.RequestDigest == b.RequestDigest && a.PlanDigest == b.PlanDigest && a.CompatibilityDigest == b.CompatibilityDigest && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.DuckDBVersion == b.DuckDBVersion && a.RuntimeVersion == b.RuntimeVersion && a.DuckLakeExtensionVersion == b.DuckLakeExtensionVersion && a.DuckLakeSpecVersion == b.DuckLakeSpecVersion && a.CatalogSchemaVersion == b.CatalogSchemaVersion && sameCanonical(a.QualificationEvidence, b.QualificationEvidence)
}

func sameCanonical(a, b []byte) bool {
	aa, err1 := canonicalObject(a, maxEvidence, true)
	bb, err2 := canonicalObject(b, maxEvidence, true)
	return err1 == nil && err2 == nil && bytes.Equal(aa, bb)
}
func loadSeal(ctx context.Context, db DBTX, id string) (SnapshotSeal, error) {
	var s SnapshotSeal
	row, err := depdb.New(db).GetSnapshotSeal(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	}
	if err != nil {
		return SnapshotSeal{}, err
	}
	s.SealID, s.AttemptID, s.CandidateID, s.PhysicalPoolID, s.TenantDomain, s.Region, s.EncryptionDomain, s.ObjectNamespace, s.CatalogDatabase, s.CatalogID, s.CatalogUUID, s.CatalogVersion, s.DuckLakeSnapshotID, s.RelationNamespace, s.RelationManifestDigest, s.ClosureDigest, s.ObjectRoot, s.ObjectRootDigest, s.ArtifactRoot, s.ArtifactRootDigest, s.CompiledGraphDigest, s.CompiledConfigDigest, s.SecurityDomainFingerprint, s.RequestDigest, s.PlanDigest, s.CompatibilityDigest, s.ServingArtifactID, s.ServingArtifactDigest, s.DuckDBVersion, s.RuntimeVersion, s.DuckLakeExtensionVersion, s.DuckLakeSpecVersion, s.CatalogSchemaVersion, s.QualificationEvidence, s.QualifiedAt = row.SealID, row.AttemptID, row.CandidateID, row.PhysicalPoolID, row.TenantDomain, row.Region, row.EncryptionDomain, row.ObjectNamespace, row.CatalogDatabase, row.CatalogID, row.CatalogUuid, row.CatalogVersion, row.DucklakeSnapshotID, row.RelationNamespace, row.RelationManifestDigest, row.ClosureDigest, row.ObjectRoot, row.ObjectRootDigest, row.ArtifactRoot, row.ArtifactRootDigest, row.CompiledGraphDigest, row.CompiledConfigDigest, row.SecurityDomainFingerprint, row.RequestDigest, row.PlanDigest, row.CompatibilityDigest, row.ServingArtifactID, row.ServingArtifactDigest, row.DuckdbVersion, row.RuntimeVersion, row.DucklakeExtensionVersion, row.DucklakeSpecVersion, row.CatalogSchemaVersion, append([]byte(nil), row.QualificationEvidence...), dbTime(row.QualifiedAt)
	return s, nil
}
func (r *Repository) SnapshotSeal(ctx context.Context, id string) (SnapshotSeal, error) {
	db, err := requireDB(r)
	if err != nil {
		return SnapshotSeal{}, err
	}
	id, err = uuidID(id, "seal id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	return loadSeal(contextOrBackground(ctx), db, id)
}

// SnapshotSealTx is the transaction-aware immutable seal projection.
func (r *Repository) SnapshotSealTx(ctx context.Context, tx Tx, id string) (SnapshotSeal, error) {
	if tx == nil {
		return SnapshotSeal{}, ErrInvalid
	}
	id, err := uuidID(id, "seal id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	return loadSeal(contextOrBackground(ctx), tx, id)
}
func (r *Repository) LoadSnapshotSeal(ctx context.Context, id string) (SnapshotSeal, error) {
	return r.SnapshotSeal(ctx, id)
}

func (r *Repository) CreatePublication(ctx context.Context, in PublicationInput) (DeliveryPublication, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryPublication{}, err
	}
	return createPublication(contextOrBackground(ctx), db, in)
}

// CreatePublicationTx records a pending publication through a caller-owned
// PostgreSQL transaction. It does not commit or roll back tx.
func (r *Repository) CreatePublicationTx(ctx context.Context, tx Tx, in PublicationInput) (DeliveryPublication, error) {
	if tx == nil {
		return DeliveryPublication{}, ErrInvalid
	}
	return createPublication(contextOrBackground(ctx), tx, in)
}

func createPublication(ctx context.Context, db DBTX, in PublicationInput) (DeliveryPublication, error) {
	id, err := uuidID(in.PublicationID, "publication id", true)
	if err != nil {
		return DeliveryPublication{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryPublication{}, err
	}
	generation, err := uuidID(in.GenerationID, "generation id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	candidate, err := uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	seal, err := uuidID(in.SnapshotSealID, "seal id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	if in.ExpectedTargetRevision <= 0 {
		return DeliveryPublication{}, ErrInvalid
	}
	baseGeneration := ""
	if in.ExpectedBaseGenerationID != "" {
		baseGeneration, err = uuidID(in.ExpectedBaseGenerationID, "expected base generation id", false)
		if err != nil {
			return DeliveryPublication{}, err
		}
	}
	if _, err := digest(in.RequestDigest, "request digest"); err != nil {
		return DeliveryPublication{}, err
	}
	actor, err := textID(in.ActorID, "actor id")
	if err != nil {
		return DeliveryPublication{}, err
	}
	// Idempotent replay is keyed by the caller-owned publication identity. Do
	// this lookup before deriving the current active pointer: a committed
	// publication may be retried after its generation became active, and must
	// still return the original expected-base generation rather than conflict
	// with the newer pointer.
	if existing, lookupErr := loadPublication(ctx, db, id); lookupErr == nil {
		if existing.TargetID != target || existing.GenerationID != generation || existing.CandidateID != candidate || existing.SnapshotSealID != seal || existing.ExpectedTargetRevision != in.ExpectedTargetRevision || existing.ActorID != actor || existing.RequestDigest != in.RequestDigest {
			return DeliveryPublication{}, ErrConflict
		}
		if in.ExpectedBaseGenerationID != "" && existing.ExpectedBaseGenerationID != baseGeneration {
			return DeliveryPublication{}, ErrConflict
		}
		return existing, nil
	} else if !errors.Is(lookupErr, ErrNotFound) {
		return DeliveryPublication{}, lookupErr
	}
	gl, err := depdb.New(db).GetGenerationLinks(ctx, dbUUID(generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	if gl.TargetID != target || gl.CandidateID != candidate || gl.SnapshotSealID != seal {
		return DeliveryPublication{}, fmt.Errorf("%w: publication generation identity differs", ErrConflict)
	}
	cs, err := depdb.New(db).GetCandidateStatus(ctx, dbUUID(candidate))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	if (cs.Status != "qualified" && cs.Status != "ready" && cs.Status != "admitted") || cs.TargetID != target || cs.SnapshotSealID != seal {
		return DeliveryPublication{}, ErrNotQualified
	}
	activeBase, err := depdb.New(db).GetActiveGeneration(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		activeBase = ""
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	if baseGeneration == "" {
		baseGeneration = activeBase
	} else if baseGeneration != activeBase {
		return DeliveryPublication{}, ErrCASConflict
	}
	err = depdb.New(db).InsertPublication(ctx, depdb.InsertPublicationParams{PublicationID: dbUUID(id), TargetID: target, GenerationID: dbUUID(generation), ExpectedBaseGenerationID: dbUUID(baseGeneration), CandidateID: dbUUID(candidate), SnapshotSealID: dbUUID(seal), ExpectedTargetRevision: in.ExpectedTargetRevision, ActorID: actor, RequestDigest: in.RequestDigest})
	if err != nil {
		return DeliveryPublication{}, err
	}
	p, err := loadPublication(ctx, db, id)
	if err != nil {
		return DeliveryPublication{}, err
	}
	if p.TargetID != target || p.GenerationID != generation || p.ExpectedBaseGenerationID != baseGeneration || p.CandidateID != candidate || p.SnapshotSealID != seal || p.ExpectedTargetRevision != in.ExpectedTargetRevision || p.ActorID != actor || p.RequestDigest != in.RequestDigest {
		return DeliveryPublication{}, ErrConflict
	}
	return p, nil
}
func loadPublication(ctx context.Context, db DBTX, id string) (DeliveryPublication, error) {
	var p DeliveryPublication
	row, err := depdb.New(db).GetPublication(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	}
	p.PublicationID, p.TargetID, p.GenerationID, p.ExpectedBaseGenerationID, p.CandidateID, p.SnapshotSealID, p.ExpectedTargetRevision, p.ResultTargetRevision, p.ActorID, p.State, p.RequestDigest, p.CreatedAt = row.PublicationID, row.TargetID, row.GenerationID, row.ExpectedBaseGenerationID, row.CandidateID, row.SnapshotSealID, row.ExpectedTargetRevision, row.ResultTargetRevision, row.ActorID, row.State, row.RequestDigest, dbTime(row.CreatedAt)
	if row.CommittedAt.Valid {
		p.CommittedAt = row.CommittedAt.Time.UTC()
	}
	return p, err
}
func (r *Repository) Publication(ctx context.Context, id string) (DeliveryPublication, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryPublication{}, err
	}
	id, err = uuidID(id, "publication id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	return loadPublication(contextOrBackground(ctx), db, id)
}

// PublicationTx reads immutable publication evidence through a caller-owned
// transaction. It is used by idempotent native command replay paths.
func (r *Repository) PublicationTx(ctx context.Context, tx Tx, id string) (DeliveryPublication, error) {
	if tx == nil {
		return DeliveryPublication{}, ErrInvalid
	}
	id, err := uuidID(id, "publication id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	return loadPublication(contextOrBackground(ctx), tx, id)
}

// CommittedPublicationTx returns the committed deployment publication for a
// generation through a caller-owned transaction. It is used by downstream
// authorities that must prove the serving generation was actually activated,
// not merely constructed.
func (r *Repository) CommittedPublicationTx(ctx context.Context, tx Tx, generationID string) (DeliveryPublication, error) {
	if tx == nil {
		return DeliveryPublication{}, ErrInvalid
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	id, err := depdb.New(tx).FindCommittedPublication(contextOrBackground(ctx), dbUUID(generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	return loadPublication(contextOrBackground(ctx), tx, id)
}

// HistoricalCommittedPublicationTx returns the immutable committed delivery
// publication for a generation through a caller-owned transaction. Unlike
// CommittedPublicationTx, this lookup intentionally ignores the active
// pointer, so completed generations remain replayable after a successor is
// activated.
func (r *Repository) HistoricalCommittedPublicationTx(ctx context.Context, tx Tx, generationID string) (DeliveryPublication, error) {
	if tx == nil {
		return DeliveryPublication{}, ErrInvalid
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	id, err := depdb.New(tx).FindHistoricalCommittedPublication(contextOrBackground(ctx), dbUUID(generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	return loadPublication(contextOrBackground(ctx), tx, id)
}

func (r *Repository) LoadPublication(ctx context.Context, id string) (DeliveryPublication, error) {
	return r.Publication(ctx, id)
}

// CancelPublication transitions a pending publication to the terminal
// rejected state.  Publication history is append-only: cancellation never
// deletes the row, and an exact replay returns the same terminal evidence.
// The transition itself is fenced by the publication row lock and is
// intentionally separate from event/audit/workflow appenders so the caller
// can compose all control-plane writes in one transaction.
func (r *Repository) CancelPublication(ctx context.Context, id string) (DeliveryPublication, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return DeliveryPublication{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	p, err := r.CancelPublicationTx(contextOrBackground(ctx), tx, id)
	if err != nil {
		return DeliveryPublication{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryPublication{}, err
	}
	committed = true
	return p, nil
}

// CancelPublicationTx performs the fenced terminal transition using the
// caller-owned transaction.  It does not append side effects or take
// transaction ownership.
func (r *Repository) CancelPublicationTx(ctx context.Context, tx Tx, id string) (DeliveryPublication, error) {
	if tx == nil {
		return DeliveryPublication{}, ErrInvalid
	}
	id, err := uuidID(id, "publication id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	if _, err := depdb.New(tx).LockPublication(ctx, dbUUID(id)); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	p, err := loadPublication(ctx, tx, id)
	if err != nil {
		return DeliveryPublication{}, err
	}
	if p.State == "rejected" {
		return p, nil
	}
	if p.State != "pending" {
		return DeliveryPublication{}, ErrConflict
	}
	rows, err := depdb.New(tx).CancelPublication(ctx, dbUUID(id))
	if err != nil {
		return DeliveryPublication{}, err
	}
	if rows != 1 {
		return DeliveryPublication{}, ErrConflict
	}
	p.State = "rejected"
	return p, nil
}

// AcquireLease creates a persisted fencing token. Target row locking makes
// concurrent owners serialize and ensures epochs are strictly increasing.
func (r *Repository) AcquireLease(ctx context.Context, in LeaseInput) (DeliveryLease, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return DeliveryLease{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	l, err := acquireLease(contextOrBackground(ctx), tx, in)
	if err != nil {
		return DeliveryLease{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryLease{}, err
	}
	committed = true
	return l, nil
}

// AcquireLeaseTx performs lease admission on a caller-owned transaction. It
// is used by native HTTP coordinators that must fence activation and complete
// operation idempotency on the same control-plane commit boundary.
func (r *Repository) AcquireLeaseTx(ctx context.Context, tx Tx, in LeaseInput) (DeliveryLease, error) {
	if tx == nil {
		return DeliveryLease{}, ErrInvalid
	}
	return acquireLease(contextOrBackground(ctx), tx, in)
}
func acquireLease(ctx context.Context, db DBTX, in LeaseInput) (DeliveryLease, error) {
	id, err := uuidID(in.LeaseID, "lease id", true)
	if err != nil {
		return DeliveryLease{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryLease{}, err
	}
	owner, err := textID(in.OwnerID, "owner id")
	if err != nil {
		return DeliveryLease{}, err
	}
	now, err := databaseNow(ctx, db)
	if err != nil {
		return DeliveryLease{}, err
	}
	// Acquisition time is authority-owned.  A caller can supply the expiry
	// deadline, but cannot smuggle a node-local timestamp into the durable
	// lease record.
	acq := now.Truncate(time.Microsecond)
	exp := in.ExpiresAt.UTC().Truncate(time.Microsecond)
	if exp.IsZero() || !exp.After(acq) || exp.After(acq.Add(maxLease)) {
		return DeliveryLease{}, ErrInvalid
	}
	var epoch int64
	if err := depdb.New(db).EnsureTargetFence(ctx, target); err != nil {
		return DeliveryLease{}, err
	}
	if epoch, err = depdb.New(db).LockTargetFence(ctx, target); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, ErrNotFound
	} else if err != nil {
		return DeliveryLease{}, err
	}
	if epoch <= 0 {
		return DeliveryLease{}, ErrConflict
	}
	// A lease ID is an idempotency key and is never reused.  Check it while
	// holding its row lock before allocating a new epoch; otherwise a retry
	// could expire the successful lease and accidentally return a conflict.
	var existing DeliveryLease
	row, err := depdb.New(db).LockLease(ctx, dbUUID(id))
	if err == nil {
		existing.LeaseID, existing.TargetID, existing.OwnerID, existing.FencingEpoch, existing.State, existing.ExpiresAt, existing.AcquiredAt = row.LeaseID, row.TargetID, row.OwnerID, row.FencingEpoch, row.State, dbTime(row.ExpiresAt), dbTime(row.AcquiredAt)
		if row.ReleasedAt.Valid {
			existing.ReleasedAt = row.ReleasedAt.Time.UTC()
		}
		if existing.TargetID != target || existing.OwnerID != owner || !existing.ExpiresAt.Equal(exp) {
			return DeliveryLease{}, ErrConflict
		}
		if existing.State == "active" && existing.ExpiresAt.After(now) {
			return existing, nil
		}
		return DeliveryLease{}, ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, err
	}
	// The newly allocated epoch supersedes every previous owner, including the
	// same owner. This update is inside the target-row lock and the partial
	// unique index guarantees at most one active lease.
	if err := depdb.New(db).ExpireLeases(ctx, target); err != nil {
		return DeliveryLease{}, err
	}
	if err := depdb.New(db).AdvanceTargetFence(ctx, depdb.AdvanceTargetFenceParams{TargetID: target, NextFencingEpoch: epoch + 1}); err != nil {
		return DeliveryLease{}, err
	}
	err = depdb.New(db).InsertLease(ctx, depdb.InsertLeaseParams{LeaseID: dbUUID(id), TargetID: target, OwnerID: owner, FencingEpoch: epoch, ExpiresAt: pgTime(exp), AcquiredAt: pgTime(acq)})
	if err != nil {
		return DeliveryLease{}, err
	}
	return loadLease(ctx, db, id, in, epoch)
}
func loadLease(ctx context.Context, db DBTX, id string, in LeaseInput, epoch int64) (DeliveryLease, error) {
	var l DeliveryLease
	row, err := depdb.New(db).GetLease(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, ErrNotFound
	}
	if err != nil {
		return DeliveryLease{}, err
	}
	l.LeaseID, l.TargetID, l.OwnerID, l.FencingEpoch, l.State, l.ExpiresAt, l.AcquiredAt = row.LeaseID, row.TargetID, row.OwnerID, row.FencingEpoch, row.State, dbTime(row.ExpiresAt), dbTime(row.AcquiredAt)
	if row.ReleasedAt.Valid {
		l.ReleasedAt = row.ReleasedAt.Time.UTC()
	}
	if l.TargetID != in.TargetID || l.OwnerID != in.OwnerID || l.FencingEpoch != epoch || l.State != "active" || !l.ExpiresAt.Equal(in.ExpiresAt.UTC().Truncate(time.Microsecond)) {
		return DeliveryLease{}, ErrConflict
	}
	return l, nil
}
func (r *Repository) Lease(ctx context.Context, id string) (DeliveryLease, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryLease{}, err
	}
	id, err = uuidID(id, "lease id", false)
	if err != nil {
		return DeliveryLease{}, err
	}
	return loadLeaseSimple(contextOrBackground(ctx), db, id)
}

// LeaseTx reads one target lease through a caller-owned transaction. It is
// provided for atomic coordinators that must verify the post-renewal value
// before committing adjacent control ledgers.
func (r *Repository) LeaseTx(ctx context.Context, tx Tx, id string) (DeliveryLease, error) {
	if tx == nil {
		return DeliveryLease{}, ErrInvalid
	}
	id, err := uuidID(id, "lease id", false)
	if err != nil {
		return DeliveryLease{}, err
	}
	return loadLeaseSimple(contextOrBackground(ctx), tx, id)
}

// LockLeaseTx acquires the same lease-row lock used by build completion and
// returns its exact current value. Callers that will also lock a build attempt
// use this boundary first so fresh completion and recovery share the canonical
// lease -> attempt -> DuckLake lock order.
func (r *Repository) LockLeaseTx(ctx context.Context, tx Tx, id string) (DeliveryLease, error) {
	if tx == nil {
		return DeliveryLease{}, ErrInvalid
	}
	id, err := uuidID(id, "lease id", false)
	if err != nil {
		return DeliveryLease{}, err
	}
	ctx = contextOrBackground(ctx)
	if _, err := depdb.New(tx).LockLease(ctx, dbUUID(id)); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, ErrNotFound
	} else if err != nil {
		return DeliveryLease{}, err
	}
	return loadLeaseSimple(ctx, tx, id)
}

func loadLeaseSimple(ctx context.Context, db DBTX, id string) (DeliveryLease, error) {
	var l DeliveryLease
	row, err := depdb.New(db).GetLease(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, ErrNotFound
	}
	if err != nil {
		return DeliveryLease{}, err
	}
	l.LeaseID, l.TargetID, l.OwnerID, l.FencingEpoch, l.State, l.ExpiresAt, l.AcquiredAt = row.LeaseID, row.TargetID, row.OwnerID, row.FencingEpoch, row.State, dbTime(row.ExpiresAt), dbTime(row.AcquiredAt)
	if row.ReleasedAt.Valid {
		l.ReleasedAt = row.ReleasedAt.Time.UTC()
	}
	return l, nil
}
func (r *Repository) ReleaseLease(ctx context.Context, f LeaseFence) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	return releaseLease(contextOrBackground(ctx), db, f)
}

// ReleaseLeaseTx releases a lease through a caller-owned PostgreSQL
// transaction. It deliberately has the exact replay and stale-fence
// semantics of ReleaseLease and never commits or rolls back tx.
func (r *Repository) ReleaseLeaseTx(ctx context.Context, tx Tx, f LeaseFence) error {
	if tx == nil {
		return ErrInvalid
	}
	return releaseLease(contextOrBackground(ctx), tx, f)
}

// ReleaseLeaseAfterAttemptTerminationTx closes the exact target lease after
// both build-attempt ledgers have reached a terminal state in the same
// transaction. Unlike ordinary release, an exact expired lease is accepted:
// heartbeat loss must not roll back the attempt and operation settlement that
// makes the stale writer non-admissible. A different owner or fence remains a
// hard stale-fence error.
func (r *Repository) ReleaseLeaseAfterAttemptTerminationTx(ctx context.Context, tx Tx, f LeaseFence) error {
	if tx == nil {
		return ErrInvalid
	}
	id, err := uuidID(f.LeaseID, "lease id", false)
	if err != nil {
		return err
	}
	target, err := textID(f.TargetID, "target id")
	if err != nil {
		return err
	}
	owner, err := textID(f.OwnerID, "owner id")
	if err != nil || f.FencingEpoch <= 0 {
		return ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	updated, err := depdb.New(tx).ReleaseLeaseAfterAttemptTermination(ctx, depdb.ReleaseLeaseAfterAttemptTerminationParams{LeaseID: dbUUID(id), TargetID: target, OwnerID: owner, FencingEpoch: f.FencingEpoch})
	if errors.Is(err, pgx.ErrNoRows) {
		updated, err = false, nil
	}
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	lease, err := loadLeaseSimple(ctx, tx, id)
	if err != nil {
		return err
	}
	if lease.OwnerID != owner || lease.TargetID != target || lease.FencingEpoch != f.FencingEpoch {
		return ErrStaleFence
	}
	if lease.State == "released" || lease.State == "expired" {
		return nil
	}
	return ErrConflict
}

func releaseLease(ctx context.Context, db DBTX, f LeaseFence) error {
	id, err := uuidID(f.LeaseID, "lease id", false)
	if err != nil {
		return err
	}
	target, err := textID(f.TargetID, "target id")
	if err != nil {
		return err
	}
	owner, err := textID(f.OwnerID, "owner id")
	if err != nil {
		return err
	}
	updated, err := depdb.New(db).ReleaseLease(ctx, depdb.ReleaseLeaseParams{LeaseID: dbUUID(id), TargetID: target, OwnerID: owner, FencingEpoch: f.FencingEpoch})
	if errors.Is(err, pgx.ErrNoRows) {
		updated, err = false, nil
	}
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	l, e := loadLeaseSimple(ctx, db, id)
	if e != nil {
		return e
	}
	if l.OwnerID != owner || l.TargetID != target || l.FencingEpoch != f.FencingEpoch {
		return ErrStaleFence
	}
	if l.State == "released" {
		return nil
	}
	return ErrStaleFence
}

// CompleteBuildTx composes the durable build completion boundary through one
// caller-owned PostgreSQL transaction. It commits (or exactly replays) the
// attempt, creates (or exactly replays) its immutable snapshot seal,
// qualifies (or exactly replays) the candidate, and releases the exact lease
// fence. The method never commits or rolls back tx; callers can append their
// event, audit, and workflow consequences before committing the transaction.
func (r *Repository) CompleteBuildTx(
	ctx context.Context,
	tx Tx,
	commit CommitAttemptInput,
	seal SnapshotSealInput,
	qualificationDigest string,
	fence LeaseFence,
) (CompleteBuildResult, error) {
	if tx == nil {
		return CompleteBuildResult{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)

	// Validate the key cross-authority identity before the first state
	// transition.
	// The individual Tx methods repeat their own checks while holding the
	// relevant row locks; this preflight prevents a caller accidentally
	// committing a partial transaction after a mismatched composition error.
	candidateID, err := validateCompleteBuildPreflight(ctx, tx, commit, seal, qualificationDigest, fence)
	if err != nil {
		return CompleteBuildResult{}, err
	}

	attempt, err := r.CommitBuildAttemptTx(ctx, tx, commit)
	if err != nil {
		return CompleteBuildResult{}, err
	}
	sealed, err := r.CreateSnapshotSealTx(ctx, tx, seal)
	if err != nil {
		return CompleteBuildResult{}, err
	}
	candidate, err := r.QualifyCandidateTx(ctx, tx, candidateID, sealed.SealID, qualificationDigest)
	if err != nil {
		return CompleteBuildResult{}, err
	}
	if err := r.ReleaseLeaseTx(ctx, tx, fence); err != nil {
		return CompleteBuildResult{}, err
	}
	lease, err := loadLeaseSimple(ctx, tx, fence.LeaseID)
	if err != nil {
		return CompleteBuildResult{}, err
	}
	return CompleteBuildResult{Attempt: attempt, Seal: sealed, Candidate: candidate, Lease: lease}, nil
}

// CompleteBuild owns a short transaction around CompleteBuildTx. Callers
// that need to append event/audit/workflow evidence atomically should use the
// Tx form directly instead.
func (r *Repository) CompleteBuild(
	ctx context.Context,
	commit CommitAttemptInput,
	seal SnapshotSealInput,
	qualificationDigest string,
	fence LeaseFence,
) (CompleteBuildResult, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.begin(ctx)
	if err != nil {
		return CompleteBuildResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	result, err := r.CompleteBuildTx(ctx, tx, commit, seal, qualificationDigest, fence)
	if err != nil {
		return CompleteBuildResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CompleteBuildResult{}, err
	}
	committed = true
	return result, nil
}

func validateCompleteBuildPreflight(
	ctx context.Context,
	tx Tx,
	commit CommitAttemptInput,
	seal SnapshotSealInput,
	qualificationDigest string,
	fence LeaseFence,
) (candidateID string, err error) {
	attemptID, err := uuidID(commit.AttemptID, "attempt id", false)
	if err != nil {
		return "", err
	}
	if _, err := uuidID(fence.LeaseID, "lease id", false); err != nil {
		return "", err
	}
	if _, err := uuidID(seal.SealID, "seal id", false); err != nil {
		return "", err
	}
	sealAttemptID, err := uuidID(seal.AttemptID, "attempt id", false)
	if err != nil {
		return "", err
	}
	if attemptID != sealAttemptID {
		return "", fmt.Errorf("%w: commit and seal attempts differ", ErrConflict)
	}
	candidateID, err = uuidID(seal.CandidateID, "candidate id", false)
	if err != nil {
		return "", err
	}
	if commit.OwnerID != fence.OwnerID || commit.FencingEpoch != fence.FencingEpoch {
		return "", fmt.Errorf("%w: commit and lease fence differ", ErrConflict)
	}
	if _, err := textID(fence.TargetID, "target id"); err != nil {
		return "", err
	}
	if _, err := textID(fence.OwnerID, "owner id"); err != nil {
		return "", err
	}
	if fence.FencingEpoch <= 0 {
		return "", ErrInvalid
	}
	if _, err := digest(qualificationDigest, "qualification digest"); err != nil {
		return "", err
	}

	// Hold the lease row lock across the full completion sequence. This keeps a
	// concurrent takeover/release from changing the fence after preflight but
	// before the final exact release.
	if _, lockErr := depdb.New(tx).LockLease(ctx, dbUUID(fence.LeaseID)); errors.Is(lockErr, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if lockErr != nil {
		return "", lockErr
	}
	lease, err := loadLeaseSimple(ctx, tx, fence.LeaseID)
	if err != nil {
		return "", err
	}
	if lease.TargetID != fence.TargetID || lease.OwnerID != fence.OwnerID || lease.FencingEpoch != fence.FencingEpoch {
		return "", ErrStaleFence
	}
	if lease.State != "active" && lease.State != "released" {
		return "", ErrStaleFence
	}
	if lease.State == "active" {
		now, nowErr := databaseNow(ctx, tx)
		if nowErr != nil {
			return "", nowErr
		}
		if !lease.ExpiresAt.After(now) {
			return "", ErrStaleFence
		}
	}

	attempt, err := loadAttempt(ctx, tx, attemptID)
	if err != nil {
		return "", err
	}
	if attempt.OwnerID != fence.OwnerID || attempt.FencingEpoch != fence.FencingEpoch {
		return "", ErrStaleFence
	}
	if lease.State == "released" && attempt.State != AttemptCommitted {
		return "", ErrStaleFence
	}
	if attempt.CandidateID == "" {
		return "", fmt.Errorf("%w: build attempt has no candidate", ErrConflict)
	}
	attemptCandidateID, parseErr := uuidID(attempt.CandidateID, "candidate id", false)
	if parseErr != nil {
		return "", parseErr
	}
	if attemptCandidateID != candidateID {
		return "", fmt.Errorf("%w: attempt and seal candidates differ", ErrConflict)
	}
	// The binding must be present before the completion transition and must
	// agree with the canonical generation identity carried by the commit
	// marker. This prevents a caller from committing an artifact without a
	// serving-state hand-off, while still permitting exact terminal replays.
	marker, _, markerErr := decodeCommitMarker(commit.CommitMarker, true)
	if markerErr != nil {
		return "", fmt.Errorf("%w: invalid commit marker: %v", ErrInvalid, markerErr)
	}
	if !markerIdentityMatches(marker, attemptID, attempt.PhysicalPoolID, attempt.RequestDigest, attempt.PlanDigest, attempt.FencingEpoch) {
		return "", fmt.Errorf("%w: commit marker identity mismatch", ErrConflict)
	}
	binding, bindingErr := loadBuildArtifactBinding(ctx, tx, attemptID)
	if errors.Is(bindingErr, ErrNotFound) {
		return "", fmt.Errorf("%w: build artifact binding is missing", ErrNotQualified)
	}
	if bindingErr != nil {
		return "", bindingErr
	}
	if binding.ServingArtifactID != seal.ServingArtifactID || binding.ServingArtifactDigest != seal.ServingArtifactDigest {
		return "", fmt.Errorf("%w: build artifact binding differs", ErrConflict)
	}
	if binding.ServingStateID != marker.GenerationID {
		return "", fmt.Errorf("%w: serving state differs from commit marker generation", ErrConflict)
	}
	candidate, err := loadCandidate(ctx, tx, candidateID, CandidateInput{})
	if err != nil {
		return "", err
	}
	if candidate.TargetID != fence.TargetID || candidate.PlanID != attempt.PlanID {
		return "", fmt.Errorf("%w: build completion target or plan differs", ErrConflict)
	}
	return candidateID, nil
}

func (r *Repository) RenewLease(ctx context.Context, f LeaseFence, expiresAt time.Time) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	id, err := uuidID(f.LeaseID, "lease id", false)
	if err != nil {
		return err
	}
	target, err := textID(f.TargetID, "target id")
	if err != nil {
		return err
	}
	owner, err := textID(f.OwnerID, "owner id")
	if err != nil {
		return err
	}
	now, err := databaseNow(contextOrBackground(ctx), db)
	if err != nil {
		return err
	}
	exp := expiresAt.UTC().Truncate(time.Microsecond)
	if !exp.After(now) || exp.After(now.Add(maxLease)) {
		return ErrInvalid
	}
	updated, err := depdb.New(db).RenewLease(contextOrBackground(ctx), depdb.RenewLeaseParams{LeaseID: dbUUID(id), TargetID: target, OwnerID: owner, FencingEpoch: f.FencingEpoch, ExpiresAt: pgTime(exp)})
	if errors.Is(err, pgx.ErrNoRows) {
		updated, err = false, nil
	}
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	l, e := loadLeaseSimple(contextOrBackground(ctx), db, id)
	if e != nil {
		return e
	}
	if l.OwnerID != owner || l.TargetID != target || l.FencingEpoch != f.FencingEpoch {
		return ErrStaleFence
	}
	return ErrLeaseExpired
}

// RenewLeaseTx renews an active target lease on a caller-owned transaction.
// The lease identity and fencing epoch are checked by the database update;
// callers may compose this operation with the operation and build-attempt
// ledgers before committing one control-plane transaction.
func (r *Repository) RenewLeaseTx(ctx context.Context, tx Tx, f LeaseFence, expiresAt time.Time) error {
	if tx == nil {
		return ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	id, err := uuidID(f.LeaseID, "lease id", false)
	if err != nil {
		return err
	}
	target, err := textID(f.TargetID, "target id")
	if err != nil {
		return err
	}
	owner, err := textID(f.OwnerID, "owner id")
	if err != nil {
		return err
	}
	if f.FencingEpoch <= 0 {
		return ErrInvalid
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return err
	}
	exp := expiresAt.UTC().Truncate(time.Microsecond)
	if !exp.After(now) || exp.After(now.Add(maxLease)) {
		return ErrInvalid
	}
	updated, err := depdb.New(tx).RenewLeaseForward(ctx, depdb.RenewLeaseForwardParams{LeaseID: dbUUID(id), TargetID: target, OwnerID: owner, FencingEpoch: f.FencingEpoch, ExpiresAt: pgTime(exp)})
	if errors.Is(err, pgx.ErrNoRows) {
		updated, err = false, nil
	}
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	l, e := loadLeaseSimple(ctx, tx, id)
	if e != nil {
		return e
	}
	if l.OwnerID != owner || l.TargetID != target || l.FencingEpoch != f.FencingEpoch {
		return ErrStaleFence
	}
	if l.State == "active" && l.ExpiresAt.After(exp) {
		return ErrConflict
	}
	return ErrLeaseExpired
}

// Activate performs target lock, lease fence check, exact seal validation,
// CAS pointer advance, publication outcome, durable event and audit insertion
// in one PostgreSQL transaction. A committed exact replay is returned even if
// the original worker's lease has since expired: the prior transition itself
// is the proof of outcome.
func (r *Repository) Activate(ctx context.Context, in ActivationInput) (ActivationResult, error) {
	if r == nil || r.audit == nil {
		return ActivationResult{}, fmt.Errorf("%w: activation audit port is required", ErrInvalid)
	}
	if r.lineage == nil {
		return ActivationResult{}, fmt.Errorf("%w: activation lineage verifier is required", ErrInvalid)
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return ActivationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	if in.LeaseID != "" {
		if _, err := uuidID(in.LeaseID, "lease id", false); err != nil {
			return ActivationResult{}, err
		}
	}
	if in.OwnerID != "" {
		if _, err := textID(in.OwnerID, "owner id"); err != nil {
			return ActivationResult{}, err
		}
	}
	if in.CorrelationID != "" {
		if _, err := uuidID(in.CorrelationID, "correlation id", false); err != nil {
			return ActivationResult{}, err
		}
	}
	result, err := r.ActivateTx(contextOrBackground(ctx), tx, in)
	if err != nil {
		_ = tx.Rollback(contextOrBackground(ctx))
		return ActivationResult{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return ActivationResult{}, err
	}
	committed = true
	return result, nil
}
func (r *Repository) ActivateTx(ctx context.Context, tx Tx, in ActivationInput) (ActivationResult, error) {
	return r.activateTx(ctx, tx, in, nil)
}

// ActivateTxWithPreCommitHook preserves ActivateTx's single-transaction
// contract while allowing application composition to install an explicit
// qualification interruption immediately before the activation CAS.
func (r *Repository) ActivateTxWithPreCommitHook(ctx context.Context, tx Tx, in ActivationInput, beforeCommit ActivationPreCommitHook) (ActivationResult, error) {
	return r.activateTx(ctx, tx, in, beforeCommit)
}

func (r *Repository) activateTx(ctx context.Context, tx Tx, in ActivationInput, beforeCommit ActivationPreCommitHook) (ActivationResult, error) {
	if r == nil || r.audit == nil {
		return ActivationResult{}, fmt.Errorf("%w: activation audit port is required", ErrInvalid)
	}
	if r.lineage == nil {
		return ActivationResult{}, fmt.Errorf("%w: activation lineage verifier is required", ErrInvalid)
	}
	if !r.EventCapable() {
		return ActivationResult{}, fmt.Errorf("%w: activation event boundary is required", ErrInvalid)
	}
	if tx == nil {
		return ActivationResult{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	pid, err := uuidID(in.PublicationID, "publication id", false)
	if err != nil {
		return ActivationResult{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return ActivationResult{}, err
	}
	gen, err := uuidID(in.GenerationID, "generation id", false)
	if err != nil {
		return ActivationResult{}, err
	}
	if in.ExpectedTargetRevision <= 0 {
		return ActivationResult{}, ErrInvalid
	}
	if _, err := digest(in.RequestDigest, "request digest"); err != nil {
		return ActivationResult{}, err
	}
	actor, err := textID(in.ActorID, "actor id")
	if err != nil {
		return ActivationResult{}, err
	}
	if in.LeaseID != "" {
		if _, err := uuidID(in.LeaseID, "lease id", false); err != nil {
			return ActivationResult{}, err
		}
	}
	if in.OwnerID != "" {
		if _, err := textID(in.OwnerID, "owner id"); err != nil {
			return ActivationResult{}, err
		}
	}
	if in.CorrelationID != "" {
		if _, err := uuidID(in.CorrelationID, "correlation id", false); err != nil {
			return ActivationResult{}, err
		}
	}
	// Lock and read the publication first. This is the lost-ack boundary: an
	// exact committed row plus pointer/event/audit evidence is replayable.
	var p DeliveryPublication
	var pubErr error
	_, pubErr = depdb.New(tx).LockPublication(ctx, dbUUID(pid))
	if pubErr != nil {
		return ActivationResult{}, pubErr
	}
	p, pubErr = loadPublication(ctx, tx, pid)
	if errors.Is(pubErr, ErrNotFound) {
		return ActivationResult{}, ErrNotFound
	}
	if pubErr != nil {
		return ActivationResult{}, pubErr
	}
	if p.TargetID != target || p.GenerationID != gen || p.ExpectedTargetRevision != in.ExpectedTargetRevision || p.RequestDigest != in.RequestDigest {
		return ActivationResult{}, ErrConflict
	}
	if p.ActorID != actor {
		return ActivationResult{}, fmt.Errorf("%w: activation actor differs from publication", ErrConflict)
	}
	if p.State == "committed" {
		if p.ResultTargetRevision <= 0 {
			return ActivationResult{}, ErrConflict
		}
		pointer, err := loadTarget(ctx, tx, target)
		if err != nil {
			return ActivationResult{}, err
		}
		if err := r.verifyActivationLineage(ctx, tx, pointer.TargetID, pointer.ProjectID, p.GenerationID); err != nil {
			return ActivationResult{}, err
		}
		event, audit, err := r.loadActivationEvidence(ctx, tx, p, actor, in.CorrelationID, pid)
		if err != nil {
			return ActivationResult{}, err
		}
		expectedPayload := activationPayload(p, p.ResultTargetRevision)
		expectedMetadata := activationMetadata(p)
		if pointer.ActiveGenerationID != p.GenerationID || pointer.ActivePublicationID != p.PublicationID || pointer.TargetRevision != p.ResultTargetRevision ||
			event.EventID != p.PublicationID || event.ScopeID != target || event.AggregateType != "delivery_target" || event.AggregateID != target || event.AggregateVersion <= 0 || event.EventType != "activation_committed" || event.SchemaVersion != 1 || event.CorrelationID != in.CorrelationID || !sameCanonical(event.Payload, expectedPayload) ||
			audit.AuditID != p.PublicationID || audit.EventID != event.EventID || audit.ScopeID != target || audit.ActorID != actor || audit.Action != "activate" || audit.ResourceKind != "generation" || audit.ResourceID != p.GenerationID || audit.Outcome != "accepted" || audit.RequestDigest != in.RequestDigest || !sameCanonical(audit.Metadata, expectedMetadata) {
			return ActivationResult{}, fmt.Errorf("%w: activation replay identity differs", ErrConflict)
		}
		return ActivationResult{Publication: p, Pointer: pointer, Event: event, Audit: audit, Replay: true}, nil
	}
	if p.State != "pending" {
		return ActivationResult{}, ErrConflict
	}
	// Verify the persisted lease and exact owner fence while holding its row.
	if in.LeaseID == "" || in.OwnerID == "" || in.FencingEpoch <= 0 {
		return ActivationResult{}, ErrStaleFence
	}
	leaseRow, err := depdb.New(tx).LockLeaseForActivation(ctx, dbUUID(in.LeaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrStaleFence
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if leaseRow.TargetID != target || leaseRow.OwnerID != in.OwnerID || leaseRow.FencingEpoch != in.FencingEpoch || leaseRow.State != "active" {
		return ActivationResult{}, ErrStaleFence
	}
	if !leaseRow.LeaseActive {
		return ActivationResult{}, ErrLeaseExpired
	}
	// Lock target and verify CAS revision.
	tr, err := depdb.New(tx).LockTargetForUpdate(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	targetRecord, err := loadTarget(ctx, tx, target)
	if err != nil {
		return ActivationResult{}, err
	}
	if err := r.verifyActivationLineage(ctx, tx, targetRecord.TargetID, targetRecord.ProjectID, p.GenerationID); err != nil {
		return ActivationResult{}, err
	}
	currentRev, currentGeneration := tr.TargetRevision, tr.ActiveGenerationID
	if currentRev != in.ExpectedTargetRevision || currentGeneration != p.ExpectedBaseGenerationID {
		return ActivationResult{}, ErrCASConflict
	}
	cr, err := depdb.New(tx).GetCandidateStatus(ctx, dbUUID(p.CandidateID))
	cstatus, ct, cs := cr.Status, cr.TargetID, cr.SnapshotSealID
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if cstatus != "qualified" && cstatus != "ready" && cstatus != "admitted" || ct != target || cs != p.SnapshotSealID {
		return ActivationResult{}, ErrNotQualified
	}
	sealProof, err := depdb.New(tx).GetSnapshotSealProof(ctx, dbUUID(p.SnapshotSealID))
	sealAttempt, sealReq, sealPlan, snap := sealProof.AttemptID, sealProof.RequestDigest, sealProof.PlanDigest, sealProof.DucklakeSnapshotID
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	genLinks, err := depdb.New(tx).GetGenerationLinks(ctx, dbUUID(p.GenerationID))
	genTarget, genCand, genSeal := genLinks.TargetID, genLinks.CandidateID, genLinks.SnapshotSealID
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if genTarget != target || genCand != p.CandidateID || genSeal != p.SnapshotSealID {
		return ActivationResult{}, fmt.Errorf("%w: generation identity differs", ErrConflict)
	}
	requiresApproval, err := depdb.New(tx).GetPlanApprovalRequired(ctx, dbUUID(p.GenerationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	} else if err != nil {
		return ActivationResult{}, err
	}
	if requiresApproval {
		if _, lockErr := depdb.New(tx).LockApprovalRequestForPublication(ctx, depdb.LockApprovalRequestForPublicationParams{
			PublicationID: dbUUID(p.PublicationID), TargetID: target, GenerationID: dbUUID(p.GenerationID), CandidateID: dbUUID(p.CandidateID),
			RequestDigest: p.RequestDigest, ExpectedTargetRevision: p.ExpectedTargetRevision,
		}); errors.Is(lockErr, pgx.ErrNoRows) {
			return ActivationResult{}, fmt.Errorf("%w: reviewer approval is required", ErrNotQualified)
		} else if lockErr != nil {
			return ActivationResult{}, lockErr
		}
		approved, err := depdb.New(tx).EffectiveApprovalForPublication(ctx, depdb.EffectiveApprovalForPublicationParams{
			PublicationID: dbUUID(p.PublicationID), TargetID: target, GenerationID: dbUUID(p.GenerationID), CandidateID: dbUUID(p.CandidateID),
			RequestDigest: p.RequestDigest, ExpectedTargetRevision: p.ExpectedTargetRevision,
		})
		if err != nil {
			return ActivationResult{}, err
		}
		if !approved {
			return ActivationResult{}, fmt.Errorf("%w: reviewer approval is required", ErrNotQualified)
		}
	}
	_ = sealReq // build request digest is bound to the attempt/plan, not publication idempotency.
	_ = sealAttempt
	_ = sealPlan
	_ = snap
	if beforeCommit != nil {
		if err := beforeCommit(ctx, p); err != nil {
			return ActivationResult{}, err
		}
	}
	// Find and lock the predecessor generation while the target row is still
	// locked. The target lock serializes activation; the SECURITY DEFINER
	// identity capability retains the root lock through this transaction, and
	// the retirement capability refuses to retire whichever generation remains
	// selected by the active pointer.
	// The first activation has no predecessor root.
	var predecessor depdb.FindLiveGenerationRootRow
	if currentGeneration != "" {
		found, rootErr := depdb.New(tx).FindLiveGenerationRoot(ctx, depdb.FindLiveGenerationRootParams{TargetID: target, GenerationID: dbUUID(currentGeneration)})
		if errors.Is(rootErr, pgx.ErrNoRows) {
			return ActivationResult{}, fmt.Errorf("%w: active generation retention root is unavailable", ErrConflict)
		}
		if rootErr != nil {
			return ActivationResult{}, rootErr
		}
		locked, rootErr := depdb.New(tx).GetRetentionRootIdentity(ctx, dbUUID(found.RootID))
		if errors.Is(rootErr, pgx.ErrNoRows) {
			return ActivationResult{}, fmt.Errorf("%w: active generation retention root is unavailable", ErrConflict)
		}
		if rootErr != nil {
			return ActivationResult{}, rootErr
		}
		if locked.TargetID != target || locked.GenerationID != currentGeneration || locked.RootKind != "generation" || locked.State != "live" {
			return ActivationResult{}, fmt.Errorf("%w: predecessor retention root identity differs", ErrConflict)
		}
		if found.RootID == "" {
			return ActivationResult{}, fmt.Errorf("%w: predecessor retention root identity is incomplete", ErrConflict)
		}
		// Keep the root ID returned by the lookup for the capability retirement
		// call; the locked identity above is revalidated before proceeding.
		predecessor = found
	}
	newRev := currentRev + 1
	targetUpdated, err := depdb.New(tx).UpdateTargetRevision(ctx, depdb.UpdateTargetRevisionParams{TargetID: target, NewRevision: newRev, ExpectedRevision: currentRev})
	if errors.Is(err, pgx.ErrNoRows) {
		targetUpdated, err = false, nil
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if !targetUpdated {
		return ActivationResult{}, ErrCASConflict
	}
	err = depdb.New(tx).UpsertActivePointer(ctx, depdb.UpsertActivePointerParams{TargetID: target, GenerationID: dbUUID(p.GenerationID), PublicationID: dbUUID(p.PublicationID)})
	if err != nil {
		return ActivationResult{}, err
	}
	// Candidate roots protect qualified preview/build output until activation
	// transfers reachability to a generation root. Retire that temporary root
	// after the pointer CAS so the capability can prove activation (or an
	// already elapsed DB-owned deadline); rollback activation may legitimately
	// find it already terminal, but only while the generation itself is retained.
	if err := r.retireCandidateRootForActivation(ctx, tx, p, target); err != nil {
		return ActivationResult{}, err
	}
	if currentGeneration != "" {
		retired, err := r.RetireRetentionRootTx(ctx, tx, predecessor.RootID)
		if err != nil {
			return ActivationResult{}, err
		}
		if retired.RootID != predecessor.RootID || retired.TargetID != target || retired.GenerationID != currentGeneration || retired.RootKind != "generation" || retired.State != "retiring" {
			return ActivationResult{}, fmt.Errorf("%w: predecessor retention root identity differs", ErrConflict)
		}
	}
	publicationUpdated, err := depdb.New(tx).CommitPublication(ctx, depdb.CommitPublicationParams{PublicationID: dbUUID(p.PublicationID), ResultRevision: pgInt8(&newRev)})
	if errors.Is(err, pgx.ErrNoRows) {
		publicationUpdated, err = false, nil
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if !publicationUpdated {
		return ActivationResult{}, ErrConflict
	}
	p.ResultTargetRevision = newRev
	if err = ensureActivationRoot(ctx, tx, p, target); err != nil {
		return ActivationResult{}, err
	}
	event, err := r.appendActivationEvent(ctx, tx, p, in, newRev, actor)
	if err != nil {
		return ActivationResult{}, err
	}
	audit, err := r.appendAudit(ctx, tx, p, event, actor)
	if err != nil {
		return ActivationResult{}, err
	}
	pointer, err := loadTarget(ctx, tx, target)
	if err != nil {
		return ActivationResult{}, err
	}
	p, err = loadPublication(ctx, tx, p.PublicationID)
	if err != nil {
		return ActivationResult{}, err
	}
	return ActivationResult{Publication: p, Pointer: pointer, Event: event, Audit: audit}, nil
}

func (r *Repository) verifyActivationLineage(ctx context.Context, tx Tx, targetID, projectID, generationID string) error {
	if r == nil || r.lineage == nil {
		return fmt.Errorf("%w: activation lineage verifier is required", ErrInvalid)
	}
	if targetID == "" || projectID == "" || generationID == "" {
		return fmt.Errorf("%w: activation lineage identity is incomplete", ErrConflict)
	}
	if err := r.lineage.VerifyActivationLineage(contextOrBackground(ctx), tx, ActivationLineageInput{TargetID: targetID, ProjectID: projectID, GenerationID: generationID}); err != nil {
		// Integrity or identity failures mean this generation is not eligible
		// for activation. Operational failures (for example cancellation or a
		// database outage) must remain retryable rather than being flattened
		// into a durable qualification conflict.
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotQualified) {
			return fmt.Errorf("%w: activation lineage verification: %w", ErrNotQualified, err)
		}
		return fmt.Errorf("activation lineage verification: %w", err)
	}
	return nil
}

// DeliveryResultError is a tiny helper used to keep stale-fence branches
// explicit while preserving the ordinary ActivationResult return shape.
func DeliveryResultError(err error) (ActivationResult, error) { return ActivationResult{}, err }

func (r *Repository) retireCandidateRootForActivation(ctx context.Context, tx Tx, p DeliveryPublication, target string) error {
	row, err := depdb.New(tx).GetRetentionRootIdentity(ctx, dbUUID(p.CandidateID))
	if errors.Is(err, pgx.ErrNoRows) {
		// Direct authority callers may construct already-retained generations
		// without the higher-level generation-admission coordinator. There is no
		// candidate root to leak in that case.
		return nil
	}
	if err != nil {
		return err
	}
	if row.TargetID != target || row.CandidateID != p.CandidateID || row.GenerationID != p.GenerationID || row.SnapshotSealID != p.SnapshotSealID || row.RootKind != "candidate" {
		return fmt.Errorf("%w: activation candidate retention root identity differs", ErrConflict)
	}
	if row.State != "live" {
		return r.RequireGenerationRootTx(ctx, tx, target, p.GenerationID)
	}
	if row.ExpiresAt.Valid {
		now, clockErr := databaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		if !row.ExpiresAt.Time.After(now) {
			return r.RequireGenerationRootTx(ctx, tx, target, p.GenerationID)
		}
	}
	retired, err := r.RetireRetentionRootTx(ctx, tx, p.CandidateID)
	if err != nil {
		return err
	}
	if retired.RootID != p.CandidateID || retired.TargetID != target || retired.CandidateID != p.CandidateID || retired.GenerationID != p.GenerationID || retired.SnapshotSealID != p.SnapshotSealID || retired.RootKind != "candidate" || retired.State != "retiring" {
		return fmt.Errorf("%w: activation candidate retention root transition differs", ErrConflict)
	}
	return nil
}

func activationPayload(p DeliveryPublication, revision int64) json.RawMessage {
	payload, _ := canonicalObject(json.RawMessage(fmt.Sprintf(`{"publication_id":%q,"generation_id":%q,"target_revision":%d}`, p.PublicationID, p.GenerationID, revision)), 65536, true)
	return payload
}

func activationMetadata(p DeliveryPublication) json.RawMessage {
	metadata, _ := canonicalObject(json.RawMessage(fmt.Sprintf(`{"generation_id":%q,"target_revision":%d}`, p.GenerationID, p.ResultTargetRevision)), 32768, true)
	return metadata
}

func ensureActivationRoot(ctx context.Context, tx Tx, p DeliveryPublication, target string) error {
	found, err := depdb.New(tx).FindLiveGenerationRoot(ctx, depdb.FindLiveGenerationRootParams{TargetID: target, GenerationID: dbUUID(p.GenerationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		err = depdb.New(tx).InsertGenerationRoot(ctx, depdb.InsertGenerationRootParams{RootID: dbUUID(generationRootID(p.PublicationID)), TargetID: target, CandidateID: dbUUID(p.CandidateID), GenerationID: dbUUID(p.GenerationID), SnapshotSealID: dbUUID(p.SnapshotSealID)})
		return err
	}
	if err != nil {
		return err
	}
	row, err := depdb.New(tx).GetRetentionRootIdentity(ctx, dbUUID(found.RootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: activation retention root disappeared", ErrConflict)
	}
	if err != nil {
		return err
	}
	if row.TargetID != target || row.CandidateID != p.CandidateID || row.GenerationID != p.GenerationID || row.SnapshotSealID != p.SnapshotSealID || row.RootKind != "generation" || row.State != "live" {
		return fmt.Errorf("%w: activation retention root identity differs", ErrConflict)
	}
	return nil
}

// generationRootID gives every successful publication its own immutable
// reachability-root identity. A later rollback can therefore establish a new
// live root for an old generation without mutating that generation's retired
// root history. The derivation is deterministic so transaction retry is
// idempotent and disjoint from rollback roots keyed directly by publication.
func generationRootID(publicationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview:delivery:generation-root:"+publicationID)).String()
}

func (r *Repository) appendActivationEvent(ctx context.Context, tx Tx, p DeliveryPublication, in ActivationInput, revision int64, actor string) (Event, error) {
	if r == nil || !r.EventCapable() {
		return Event{}, fmt.Errorf("%w: activation event boundary is required", ErrInvalid)
	}
	payload := activationPayload(p, revision)
	e, err := r.events.AppendEvent(ctx, tx, eventspostgres.EventInput{EventID: p.PublicationID, ScopeID: in.TargetID, AggregateType: "delivery_target", AggregateID: in.TargetID, EventType: "activation_committed", SchemaVersion: 1, CorrelationID: in.CorrelationID, Payload: payload})
	if err != nil {
		return Event{}, err
	}
	if e.EventID != p.PublicationID || e.ScopeID != in.TargetID || e.AggregateType != "delivery_target" || e.AggregateID != in.TargetID || e.AggregateVersion <= 0 || e.EventType != "activation_committed" || e.SchemaVersion != 1 || e.CorrelationID != in.CorrelationID || !sameCanonical(e.Payload, payload) {
		return Event{}, fmt.Errorf("%w: activation event identity differs", ErrConflict)
	}
	return Event{EventID: e.EventID, ScopeID: e.ScopeID, AggregateType: e.AggregateType, AggregateID: e.AggregateID, AggregateVersion: e.AggregateVersion, EventType: e.EventType, SchemaVersion: e.SchemaVersion, OccurredAt: e.OccurredAt, CorrelationID: e.CorrelationID, Payload: append([]byte(nil), e.Payload...)}, nil
}
func (r *Repository) appendAudit(ctx context.Context, tx Tx, p DeliveryPublication, e Event, actor string) (AuditEvent, error) {
	metadata := activationMetadata(p)
	input := ActivationAuditInput{EventID: p.PublicationID, DomainEventID: e.EventID, ScopeID: e.ScopeID, ActorID: actor, Action: "activate", ResourceKind: "generation", ResourceID: p.GenerationID, Outcome: "accepted", RequestDigest: p.RequestDigest, CorrelationID: e.CorrelationID, AggregateKey: e.AggregateID, AggregateSequence: e.AggregateVersion, Metadata: metadata}
	a, err := r.audit.AppendActivationAudit(ctx, tx, input)
	if err != nil {
		return AuditEvent{}, normalizeActivationAuditError(err, "append")
	}
	if !sameActivationAudit(a, input) {
		return AuditEvent{}, fmt.Errorf("%w: activation audit identity differs", ErrConflict)
	}
	return a, nil
}

func (r *Repository) loadActivationEvidence(ctx context.Context, tx Tx, p DeliveryPublication, actor, correlationID, publicationID string) (Event, AuditEvent, error) {
	if r == nil || r.events == nil {
		return Event{}, AuditEvent{}, fmt.Errorf("%w: activation event authority is required", ErrInvalid)
	}
	event, err := r.events.GetEvent(ctx, tx, publicationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, AuditEvent{}, fmt.Errorf("%w: activation event evidence missing", ErrConflict)
	}
	if err != nil {
		return Event{}, AuditEvent{}, err
	}
	e := Event{EventID: event.EventID, ScopeID: event.ScopeID, AggregateType: event.AggregateType, AggregateID: event.AggregateID, AggregateVersion: event.AggregateVersion, EventType: event.EventType, SchemaVersion: event.SchemaVersion, OccurredAt: event.OccurredAt, CorrelationID: event.CorrelationID, Payload: append([]byte(nil), event.Payload...)}
	input := ActivationAuditInput{EventID: p.PublicationID, DomainEventID: event.EventID, ScopeID: event.ScopeID, ActorID: actor, Action: "activate", ResourceKind: "generation", ResourceID: p.GenerationID, Outcome: "accepted", RequestDigest: p.RequestDigest, CorrelationID: correlationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: activationMetadata(p)}
	a, err := r.audit.GetActivationAudit(ctx, tx, input)
	if err != nil {
		return Event{}, AuditEvent{}, normalizeActivationAuditError(err, "read")
	}
	if !sameActivationAudit(a, input) {
		return Event{}, AuditEvent{}, fmt.Errorf("%w: activation audit canonical identity differs", ErrConflict)
	}
	return e, a, nil
}

func sameActivationAudit(a AuditEvent, in ActivationAuditInput) bool {
	return a.AuditID == in.EventID && a.EventID == in.DomainEventID && a.ScopeID == in.ScopeID && a.ActorID == in.ActorID && a.Action == in.Action && a.ResourceKind == in.ResourceKind && a.ResourceID == in.ResourceID && a.Outcome == in.Outcome && a.RequestDigest == in.RequestDigest && sameCanonical(a.Metadata, in.Metadata)
}

func normalizeActivationAuditError(err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrConflict) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: activation audit evidence missing during %s", ErrConflict, operation)
	}
	return err
}

// Retention roots are reachability records, not seal identity.  Seals remain
// immutable historical evidence even after their physical snapshot expires.
func (r *Repository) CreateRetentionRoot(ctx context.Context, root DeliveryRetentionRoot) (DeliveryRetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	return createRetentionRoot(contextOrBackground(ctx), db, root)
}

// CreateRetentionRootTx records an immutable live retention root through a
// caller-owned transaction. It is used to keep a rollback generation
// reachable in the same commit as its pending publication request.
func (r *Repository) CreateRetentionRootTx(ctx context.Context, tx Tx, root DeliveryRetentionRoot) (DeliveryRetentionRoot, error) {
	if tx == nil {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	return createRetentionRoot(contextOrBackground(ctx), tx, root)
}

func createRetentionRoot(ctx context.Context, db DBTX, root DeliveryRetentionRoot) (DeliveryRetentionRoot, error) {
	id, err := uuidID(root.RootID, "root id", true)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	target, err := textID(root.TargetID, "target id")
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if root.RootKind == "" {
		root.RootKind = "generation"
	}
	if root.State == "" {
		root.State = "live"
	}
	if root.GenerationID != "" {
		if root.GenerationID, err = uuidID(root.GenerationID, "generation id", false); err != nil {
			return DeliveryRetentionRoot{}, err
		}
	}
	if root.CandidateID != "" {
		if root.CandidateID, err = uuidID(root.CandidateID, "candidate id", false); err != nil {
			return DeliveryRetentionRoot{}, err
		}
	}
	if root.SnapshotSealID != "" {
		if root.SnapshotSealID, err = uuidID(root.SnapshotSealID, "snapshot seal id", false); err != nil {
			return DeliveryRetentionRoot{}, err
		}
	}
	if root.RootKind != "candidate" && root.RootKind != "generation" && root.RootKind != "rollback" && root.RootKind != "recovery" && root.RootKind != "query" {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	if root.State != "live" {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	if (root.RootKind == "candidate" || root.RootKind == "generation") && (root.CandidateID == "" || root.GenerationID == "" || root.SnapshotSealID == "") {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	if root.RootKind == "recovery" && (root.GenerationID == "" || root.SnapshotSealID == "" || root.ExpiresAt.IsZero()) {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	if !root.ExpiresAt.IsZero() {
		root.ExpiresAt = root.ExpiresAt.UTC().Truncate(time.Microsecond)
	}
	evidence, err := canonicalObject(root.Evidence, 16384, true)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if root.RootKind == "recovery" {
		// Recovery roots are maintenance-owned. Their table intentionally grants
		// only SELECT to the maintenance role, so creation crosses the narrow
		// SECURITY DEFINER capability function rather than broadening INSERT.
		accepted, callErr := depdb.New(db).CreateRecoveryRetentionRoot(contextOrBackground(ctx), depdb.CreateRecoveryRetentionRootParams{RootID: dbUUID(id), TargetID: target, GenerationID: dbUUID(root.GenerationID), SnapshotSealID: dbUUID(root.SnapshotSealID), ExpiresAt: nullablePgTime(root.ExpiresAt), Evidence: evidence})
		err = callErr
		if err == nil && !accepted {
			err = ErrConflict
		}
	} else {
		err = depdb.New(db).InsertRetentionRoot(contextOrBackground(ctx), depdb.InsertRetentionRootParams{RootID: dbUUID(id), TargetID: target, CandidateID: dbUUID(root.CandidateID), GenerationID: dbUUID(root.GenerationID), SnapshotSealID: dbUUID(root.SnapshotSealID), RootKind: root.RootKind, State: root.State, ExpiresAt: nullablePgTime(root.ExpiresAt), Evidence: evidence})
	}
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	persisted, err := loadRetentionRoot(contextOrBackground(ctx), db, id)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if root.RootKind == "recovery" {
		// The SECURITY DEFINER function derives candidate_id from the exact
		// generation tuple. Maintenance callers are not granted SELECT on
		// delivery_generation, so load the persisted root (which is readable)
		// before applying the generic exact-replay comparison.
		if persisted.CandidateID == "" {
			return DeliveryRetentionRoot{}, ErrConflict
		}
		root.CandidateID = persisted.CandidateID
	}
	if persisted.TargetID != target || persisted.CandidateID != root.CandidateID || persisted.GenerationID != root.GenerationID || persisted.SnapshotSealID != root.SnapshotSealID || persisted.RootKind != root.RootKind || persisted.State != root.State || !sameCanonical(persisted.Evidence, evidence) || !nullableTimesEqual(persisted.ExpiresAt, root.ExpiresAt) {
		return DeliveryRetentionRoot{}, ErrConflict
	}
	return persisted, nil
}

// RetireRetentionRoot transitions one live retention root to retiring through
// the delivery capability function. The function takes a row lock before the
// transition, sharing the lock used by serving-state reader admission; this
// closes the live-root admission race. Replaying an already-retiring root is
// idempotent, while terminal or missing roots fail closed.
func (r *Repository) RetireRetentionRootTx(ctx context.Context, tx Tx, rootID string) (DeliveryRetentionRoot, error) {
	if tx == nil {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	id, err := uuidID(rootID, "root id", false)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	ctx = contextOrBackground(ctx)
	locked, err := depdb.New(tx).GetRetentionRootIdentity(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryRetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if locked.State == "expired" {
		// Retirement intent is already satisfied. Treat terminal replay as
		// success so publication/cancellation replays keep working after the
		// maintenance authority has expired the temporary root.
		return loadRetentionRoot(ctx, tx, id)
	}
	if locked.State != "live" && locked.State != "retiring" {
		return DeliveryRetentionRoot{}, fmt.Errorf("%w: invalid retention root lifecycle state", ErrConflict)
	}
	transitioned, err := depdb.New(tx).RetireRetentionRoot(ctx, dbUUID(id))
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if !transitioned {
		return DeliveryRetentionRoot{}, fmt.Errorf("%w: retention root retirement race", ErrConflict)
	}
	return loadRetentionRoot(ctx, tx, id)
}

// ExpireRetentionRootTx advances a retiring root to expired after its
// explicit expiry and caller-supplied grace have elapsed on the PostgreSQL
// clock, and only after exact serving_state reader leases have drained. A
// zero grace interval is accepted for maintenance callers that have already
// enforced an external drain window. Replaying an expired root is idempotent.
func (r *Repository) ExpireRetentionRootTx(ctx context.Context, tx Tx, rootID string, grace ...time.Duration) (DeliveryRetentionRoot, error) {
	if tx == nil {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	id, err := uuidID(rootID, "root id", false)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if len(grace) > 1 || (len(grace) == 1 && grace[0] < 0) {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	interval := pgtype.Interval{Valid: true}
	if len(grace) == 1 {
		interval.Microseconds = grace[0].Microseconds()
	}
	ctx = contextOrBackground(ctx)
	locked, err := depdb.New(tx).GetRetentionRootIdentity(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryRetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if locked.State == "live" {
		return DeliveryRetentionRoot{}, fmt.Errorf("%w: live retention root must be retired before expiry", ErrConflict)
	}
	if locked.State != "retiring" && locked.State != "expired" {
		return DeliveryRetentionRoot{}, fmt.Errorf("%w: invalid retention root lifecycle state", ErrConflict)
	}
	transitioned, err := depdb.New(tx).ExpireRetentionRoot(ctx, depdb.ExpireRetentionRootParams{RootID: dbUUID(id), Grace: interval})
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if !transitioned {
		// The capability function deliberately returns false for an unelapsed
		// grace/expiry window, active reader leases, or corrupt evidence. Do not
		// treat any of those as success: callers must retry after drain/time.
		return DeliveryRetentionRoot{}, fmt.Errorf("%w: retention root is not ready for expiry", ErrConflict)
	}
	return loadRetentionRoot(ctx, tx, id)
}

// RetireRetentionRoot owns a transaction for callers that do not need to
// compose retirement with another delivery mutation.
func (r *Repository) RetireRetentionRoot(ctx context.Context, rootID string) (DeliveryRetentionRoot, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	root, err := r.RetireRetentionRootTx(ctx, tx, rootID)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryRetentionRoot{}, err
	}
	committed = true
	return root, nil
}

// ExpireRetentionRoot owns a transaction for standalone maintenance callers.
func (r *Repository) ExpireRetentionRoot(ctx context.Context, rootID string, grace ...time.Duration) (DeliveryRetentionRoot, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	root, err := r.ExpireRetentionRootTx(ctx, tx, rootID, grace...)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryRetentionRoot{}, err
	}
	committed = true
	return root, nil
}

// Drain retires at most limit due candidate roots and expires at most limit
// ready retiring roots. PostgreSQL owns time, locking, active-generation
// protection, and exact reader-lease checks inside one bounded owner function.
func (m *Maintenance) Drain(ctx context.Context, physicalPoolID, catalogID string, grace time.Duration, limit int) (RetentionDrainResult, error) {
	if m == nil || m.db == nil || grace < 0 || limit < 1 || limit > 1000 {
		return RetentionDrainResult{}, ErrInvalid
	}
	physicalPool, err := textID(physicalPoolID, "physical pool id")
	if err != nil {
		return RetentionDrainResult{}, err
	}
	catalog, err := textID(catalogID, "catalog id")
	if err != nil {
		return RetentionDrainResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	row, err := depdb.New(m.db).MaintainRetentionRoots(ctx, depdb.MaintainRetentionRootsParams{
		PhysicalPoolID: physicalPool,
		CatalogID:      catalog,
		Grace:          pgtype.Interval{Microseconds: grace.Microseconds(), Valid: true},
		Batch:          int32(limit),
	})
	if err != nil {
		return RetentionDrainResult{}, err
	}
	if row.Retired < 0 || row.Retired > int64(limit) || row.Expired < 0 || row.Expired > int64(limit) {
		return RetentionDrainResult{}, fmt.Errorf("%w: invalid retention drain evidence", ErrConflict)
	}
	return RetentionDrainResult{Retired: row.Retired, Expired: row.Expired}, nil
}

func nullableTimesEqual(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && b.IsZero()
	}
	return a.Equal(b.UTC())
}
func nullablePgTime(t time.Time) pgtype.Timestamptz {
	return pgTime(t)
}
