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
	"strings"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
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
	maxLease    = 24 * time.Hour
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

type TargetInput struct {
	TargetID, ProjectID, Environment string
	TargetRevision                   int64
}
type DeliveryTargetInput = TargetInput

type DeliveryPlan struct {
	PlanID, TargetID, PlanDigest                                         string
	PlanRevision                                                         int64
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint string
	ArtifactDigest                                                       string
	QualificationRequired                                                bool
	Evidence                                                             json.RawMessage
	CreatedAt                                                            time.Time
}
type PlanInput = DeliveryPlan
type DeliveryPlanInput = DeliveryPlan

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
	PhysicalPoolID                    string
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
	PhysicalPoolID                 string
	FencingEpoch                   int64
	RequestDigest, PlanDigest      string
	Namespace, SessionIdentity     string
	LeaseExpiresAt                 time.Time
}
type DeliveryBuildAttemptInput = BuildAttemptInput

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
type DeliverySnapshotSeal = SnapshotSeal

type DeliveryCandidate struct {
	CandidateID, TargetID, PlanID, AttemptID, SnapshotSealID string
	Status                                                   string
	CandidateRevision                                        int64
	ArtifactDigest, QualificationDigest                      string
	CreatedAt, QualifiedAt, RetiredAt                        time.Time
}
type CandidateInput = DeliveryCandidate
type DeliveryCandidateInput = DeliveryCandidate

type DeliveryGeneration struct {
	GenerationID, TargetID, CandidateID, SnapshotSealID, PlanID          string
	PlanDigest, ArtifactRoot, ArtifactRootDigest, ServingArtifactDigest  string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint string
	GenerationRevision                                                   int64
	CreatedAt                                                            time.Time
}
type GenerationInput = DeliveryGeneration
type DeliveryGenerationInput = DeliveryGeneration

type DeliveryPublication struct {
	PublicationID, TargetID, GenerationID, ExpectedBaseGenerationID, CandidateID, SnapshotSealID string
	ExpectedTargetRevision, ResultTargetRevision                                                 int64
	ActorID, State, RequestDigest                                                                string
	CreatedAt, CommittedAt                                                                       time.Time
}
type PublicationInput = DeliveryPublication
type DeliveryPublicationInput = DeliveryPublication

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

type DeliveryApproval struct {
	ApprovalID, CandidateID, PrincipalID, Decision string
	Evidence                                       json.RawMessage
	DecidedAt                                      time.Time
}

func loadApproval(ctx context.Context, db DBTX, id string) (DeliveryApproval, error) {
	a := DeliveryApproval{}
	row, err := depdb.New(db).GetApproval(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryApproval{}, ErrNotFound
	}
	if err != nil {
		return DeliveryApproval{}, err
	}
	a.ApprovalID, a.CandidateID, a.PrincipalID, a.Decision, a.Evidence, a.DecidedAt = row.ApprovalID, row.CandidateID, row.PrincipalID, row.Decision, append([]byte(nil), row.Evidence...), dbTime(row.DecidedAt)
	return a, nil
}

// ApproveCandidate records immutable reviewer evidence. Decisions are append
// only; activation considers the most recent decision for the exact candidate.
func (r *Repository) ApproveCandidate(ctx context.Context, in DeliveryApproval) (DeliveryApproval, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryApproval{}, err
	}
	id, err := uuidID(in.ApprovalID, "approval id", true)
	if err != nil {
		return DeliveryApproval{}, err
	}
	candidate, err := uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return DeliveryApproval{}, err
	}
	if in.Decision != "approved" && in.Decision != "denied" && in.Decision != "withdrawn" {
		return DeliveryApproval{}, ErrInvalid
	}
	evidence, err := canonicalObject(in.Evidence, 16384, true)
	if err != nil {
		return DeliveryApproval{}, ErrInvalid
	}
	principalID := ""
	if strings.TrimSpace(in.PrincipalID) != "" {
		principalID, err = uuidID(in.PrincipalID, "principal id", false)
		if err != nil {
			return DeliveryApproval{}, err
		}
	}
	err = depdb.New(db).InsertApproval(contextOrBackground(ctx), depdb.InsertApprovalParams{ApprovalID: dbUUID(id), CandidateID: dbUUID(candidate), PrincipalID: dbUUID(principalID), Decision: in.Decision, Evidence: evidence})
	if err != nil {
		return DeliveryApproval{}, err
	}
	a, err := loadApproval(contextOrBackground(ctx), db, id)
	if err != nil {
		return DeliveryApproval{}, err
	}
	if a.CandidateID != candidate || a.PrincipalID != principalID || a.Decision != in.Decision || !sameCanonical(a.Evidence, evidence) {
		return DeliveryApproval{}, ErrConflict
	}
	return a, nil
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

// Options wires deployment's transactional side effects. The audit port is
// required by activation; other delivery operations remain usable without
// one for isolated persistence tests.
type Options struct {
	ActivationAudit ActivationAuditPort
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
type ActivateInput = ActivationInput

type ActivationResult struct {
	Publication DeliveryPublication
	Pointer     DeliveryTarget
	Event       Event
	Audit       AuditEvent
	Replay      bool
}

type Repository struct {
	db    DBTX
	audit ActivationAuditPort
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

func New(db DBTX) *Repository { return &Repository{db: db} }

// NewWithOptions constructs a delivery repository with its composition-owned
// activation audit adapter. A nil adapter is allowed for read/build-only
// repository use, but Activate/ActivateTx fail closed when it is absent.
func NewWithOptions(db DBTX, options Options) *Repository {
	return &Repository{db: db, audit: options.ActivationAudit}
}

// NewWithActivationAudit is a concise constructor for composition packages.
func NewWithActivationAudit(db DBTX, audit ActivationAuditPort) *Repository {
	return &Repository{db: db, audit: audit}
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
func (r *Repository) Configured() bool { return r != nil && r.db != nil }

// TransactionCapable reports whether the native handle can begin the
// caller-owned control-plane transactions required by activation, leasing,
// and atomic candidate admission.
func (r *Repository) TransactionCapable() bool {
	if r == nil || r.db == nil {
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

func requireDB(r *Repository) (DBTX, error) {
	if r == nil || r.db == nil {
		return nil, ErrInvalid
	}
	return r.db, nil
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

func canonicalNonEmpty(raw json.RawMessage, max int) (json.RawMessage, error) {
	return canonicalObject(raw, max, false)
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
func createPlan(ctx context.Context, db DBTX, in PlanInput) (DeliveryPlan, error) {
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
	for label, value := range map[string]string{"plan digest": in.PlanDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "artifact digest": in.ArtifactDigest} {
		if _, err := digest(value, label); err != nil {
			return DeliveryPlan{}, err
		}
	}
	evidence, err := canonicalObject(in.Evidence, 65536, true)
	if err != nil {
		return DeliveryPlan{}, fmt.Errorf("%w: plan evidence", ErrInvalid)
	}
	err = depdb.New(db).InsertPlan(ctx, depdb.InsertPlanParams{PlanID: dbUUID(id), TargetID: target, PlanRevision: in.PlanRevision, PlanDigest: in.PlanDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, ArtifactDigest: in.ArtifactDigest, QualificationRequired: in.QualificationRequired, Evidence: evidence})
	if err != nil {
		return DeliveryPlan{}, err
	}
	return loadPlan(ctx, db, id, in)
}
func loadPlan(ctx context.Context, db DBTX, id string, expected PlanInput) (DeliveryPlan, error) {
	var p DeliveryPlan
	row, err := depdb.New(db).GetPlan(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p.PlanID, p.TargetID, p.PlanRevision, p.PlanDigest, p.CompiledGraphDigest, p.CompiledConfigDigest, p.SecurityDomainFingerprint, p.ArtifactDigest, p.QualificationRequired, p.CreatedAt = row.PlanID, row.TargetID, row.PlanRevision, row.PlanDigest, row.CompiledGraphDigest, row.CompiledConfigDigest, row.SecurityDomainFingerprint, row.ArtifactDigest, row.QualificationRequired, dbTime(row.CreatedAt)
	p.Evidence = append([]byte(nil), row.Evidence...)
	expectedEvidence, _ := canonicalObject(expected.Evidence, 65536, true)
	if p.TargetID != expected.TargetID || p.PlanRevision != expected.PlanRevision || p.PlanDigest != expected.PlanDigest || p.CompiledGraphDigest != expected.CompiledGraphDigest || p.CompiledConfigDigest != expected.CompiledConfigDigest || p.SecurityDomainFingerprint != expected.SecurityDomainFingerprint || p.ArtifactDigest != expected.ArtifactDigest || p.QualificationRequired != expected.QualificationRequired || !sameCanonical(p.Evidence, expectedEvidence) {
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
	p := DeliveryPlan{PlanID: row.PlanID, TargetID: row.TargetID, PlanRevision: row.PlanRevision, PlanDigest: row.PlanDigest, CompiledGraphDigest: row.CompiledGraphDigest, CompiledConfigDigest: row.CompiledConfigDigest, SecurityDomainFingerprint: row.SecurityDomainFingerprint, ArtifactDigest: row.ArtifactDigest, QualificationRequired: row.QualificationRequired, CreatedAt: dbTime(row.CreatedAt), Evidence: append([]byte(nil), row.Evidence...)}
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
	p := DeliveryPlan{PlanID: row.PlanID, TargetID: row.TargetID, PlanRevision: row.PlanRevision, PlanDigest: row.PlanDigest, CompiledGraphDigest: row.CompiledGraphDigest, CompiledConfigDigest: row.CompiledConfigDigest, SecurityDomainFingerprint: row.SecurityDomainFingerprint, ArtifactDigest: row.ArtifactDigest, QualificationRequired: row.QualificationRequired, CreatedAt: dbTime(row.CreatedAt), Evidence: append([]byte(nil), row.Evidence...)}
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
	if in.PhysicalPoolID == "" || in.PhysicalPoolID != strings.TrimSpace(in.PhysicalPoolID) || len(in.PhysicalPoolID) > 255 {
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
	err = depdb.New(db).InsertBuildAttempt(ctx, depdb.InsertBuildAttemptParams{AttemptID: dbUUID(id), PlanID: dbUUID(plan), CandidateID: dbUUID(candidate), OwnerID: owner, PhysicalPoolID: in.PhysicalPoolID, FencingEpoch: in.FencingEpoch, RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, Namespace: in.Namespace, LeaseExpiresAt: pgTime(lease), SessionIdentity: in.SessionIdentity})
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	a, err := loadAttempt(ctx, db, id)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if a.PlanID != plan || a.CandidateID != candidate || a.OwnerID != owner || a.PhysicalPoolID != in.PhysicalPoolID || a.FencingEpoch != in.FencingEpoch || a.RequestDigest != in.RequestDigest || a.PlanDigest != in.PlanDigest || a.Namespace != in.Namespace || a.SessionIdentity != in.SessionIdentity || !a.LeaseExpiresAt.Equal(lease) {
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
	a.AttemptID, a.PlanID, a.CandidateID, a.OwnerID, a.PhysicalPoolID, a.FencingEpoch, a.RequestDigest, a.PlanDigest, a.State, a.Namespace, a.LeaseExpiresAt, a.SessionIdentity, a.SnapshotID, a.CreatedAt, a.UpdatedAt = row.AttemptID, row.PlanID, row.CandidateID, row.OwnerID, row.PhysicalPoolID, row.FencingEpoch, row.RequestDigest, row.PlanDigest, BuildAttemptState(row.State), row.Namespace, dbTime(row.LeaseExpiresAt), row.SessionIdentity, row.SnapshotID, dbTime(row.CreatedAt), dbTime(row.UpdatedAt)
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

func (r *Repository) CommitBuildAttempt(ctx context.Context, in CommitAttemptInput) (DeliveryBuildAttempt, error) {
	_, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return r.transitionAttempt(contextOrBackground(ctx), in, AttemptCommitted)
}
func (r *Repository) AbortBuildAttempt(ctx context.Context, in TerminateAttemptInput) (DeliveryBuildAttempt, error) {
	_, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return r.transitionAttempt(contextOrBackground(ctx), CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, CommitMarker: in.Evidence}, AttemptAborted)
}
func (r *Repository) MarkAttemptIndeterminate(ctx context.Context, in TerminateAttemptInput) (DeliveryBuildAttempt, error) {
	_, err := requireDB(r)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return r.transitionAttempt(contextOrBackground(ctx), CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, CommitMarker: in.Evidence}, AttemptIndeterminate)
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
	var inputMarker ducklake.CommitMarker
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
	leaseActive, err := depdb.New(db).BuildAttemptLeaseActive(ctx, dbUUID(id))
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if !leaseActive {
		return DeliveryBuildAttempt{}, ErrLeaseExpired
	}
	if state == AttemptCommitted {
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

func markerIdentityMatches(m ducklake.CommitMarker, attempt, physicalPool, request, plan string, fence int64) bool {
	return m.AttemptID == attempt && m.PhysicalPoolID == physicalPool && m.RequestDigest == request && m.PlanDigest == plan && m.LeaseEpoch == fence
}

// decodeCommitMarker performs the full DuckLake marker decode and Normalize
// validation. PostgreSQL jsonb does not preserve input key order, so callers
// validating a stored marker must set requireCanonical to false and compare
// the normalized value semantically. The initial commit input is required to
// use DuckLake's canonical byte ordering because that exact string is written
// to commit_extra_info by the DuckLake writer.
func decodeCommitMarker(raw []byte, requireCanonical bool) (ducklake.CommitMarker, []byte, error) {
	if len(raw) == 0 {
		return ducklake.CommitMarker{}, nil, errors.New("commit marker is empty")
	}
	if len(raw) > ducklake.MaxCommitMarkerBytes {
		return ducklake.CommitMarker{}, nil, fmt.Errorf("commit marker exceeds %d bytes", ducklake.MaxCommitMarkerBytes)
	}
	var marker ducklake.CommitMarker
	if err := strictjson.DecodeWithOptions(raw, &marker, strictjson.Options{MaxBytes: ducklake.MaxCommitMarkerBytes}); err != nil {
		return ducklake.CommitMarker{}, nil, err
	}
	normalized, err := marker.Normalize()
	if err != nil {
		return ducklake.CommitMarker{}, nil, err
	}
	canonical, err := normalized.CanonicalJSON()
	if err != nil {
		return ducklake.CommitMarker{}, nil, err
	}
	canonicalBytes := []byte(canonical)
	if requireCanonical && !bytes.Equal(raw, canonicalBytes) {
		return ducklake.CommitMarker{}, nil, errors.New("commit marker is not canonical JSON")
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
	if at.State != AttemptCommitted || at.SnapshotID != in.DuckLakeSnapshotID || at.RequestDigest != in.RequestDigest || at.PlanDigest != in.PlanDigest || at.CandidateID != candidate || at.PhysicalPoolID != in.PhysicalPoolID || at.Namespace != in.RelationNamespace {
		return SnapshotSeal{}, fmt.Errorf("%w: attempt is not exact committed evidence", ErrNotQualified)
	}
	if !markerMatches(at.CommitMarker, attempt, at.PhysicalPoolID, in.RequestDigest, in.PlanDigest, at.FencingEpoch) {
		return SnapshotSeal{}, fmt.Errorf("%w: commit marker is incomplete", ErrNotQualified)
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

// CreateCandidate creates the mutable admission projection.  Qualification is
// a separate operation so a partial/missing seal cannot be published.
func (r *Repository) CreateCandidate(ctx context.Context, in CandidateInput) (DeliveryCandidate, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return createCandidate(contextOrBackground(ctx), db, in)
}

// CreateCandidateTx persists a candidate through a caller-owned control-plane
// transaction. It deliberately never commits or rolls back tx, allowing
// candidate admission to share the project-claim/audit/workflow boundary when
// the composition root has all authorities on the same PostgreSQL database.
func (r *Repository) CreateCandidateTx(ctx context.Context, tx Tx, in CandidateInput) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	return createCandidate(contextOrBackground(ctx), tx, in)
}

// StartCandidateWithClaimTx composes the instance project claim and native
// candidate admission in one caller-owned control-plane transaction. It is
// the supported atomic seam for composition roots that need candidate start
// and claim/audit evidence to commit together.
func (r *Repository) StartCandidateWithClaimTx(ctx context.Context, tx Tx, claim deployment.ProjectClaimInput, in CandidateInput) (deployment.ProjectClaim, DeliveryCandidate, error) {
	if tx == nil {
		return deployment.ProjectClaim{}, DeliveryCandidate{}, ErrInvalid
	}
	projectClaim, err := r.ClaimProjectTx(ctx, tx, claim)
	if err != nil {
		return deployment.ProjectClaim{}, DeliveryCandidate{}, err
	}
	candidate, err := r.CreateCandidateTx(ctx, tx, in)
	if err != nil {
		return deployment.ProjectClaim{}, DeliveryCandidate{}, err
	}
	return projectClaim, candidate, nil
}
func createCandidate(ctx context.Context, db DBTX, in CandidateInput) (DeliveryCandidate, error) {
	id, err := uuidID(in.CandidateID, "candidate id", true)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryCandidate{}, err
	}
	plan, err := uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if in.SnapshotSealID != "" {
		if _, err := uuidID(in.SnapshotSealID, "snapshot seal id", false); err != nil {
			return DeliveryCandidate{}, err
		}
	}
	if in.CandidateRevision <= 0 {
		return DeliveryCandidate{}, ErrInvalid
	}
	if _, err := digest(in.ArtifactDigest, "artifact digest"); err != nil {
		return DeliveryCandidate{}, err
	}
	planTarget, err := depdb.New(db).GetPlanTarget(ctx, dbUUID(plan))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	} else if err != nil {
		return DeliveryCandidate{}, err
	} else if planTarget != target {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate target differs from plan target", ErrConflict)
	}
	status := in.Status
	if status == "" {
		status = "building"
	}
	if status != "building" {
		return DeliveryCandidate{}, ErrInvalid
	}
	var qualificationDigest *string
	if in.QualificationDigest != "" {
		qualificationDigest = &in.QualificationDigest
	}
	err = depdb.New(db).InsertCandidate(ctx, depdb.InsertCandidateParams{CandidateID: dbUUID(id), TargetID: target, PlanID: dbUUID(plan), SnapshotSealID: dbUUID(in.SnapshotSealID), Status: status, CandidateRevision: in.CandidateRevision, ArtifactDigest: in.ArtifactDigest, QualificationDigest: pgText(qualificationDigest)})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(ctx, db, id, in)
}
func loadCandidate(ctx context.Context, db DBTX, id string, expected CandidateInput) (DeliveryCandidate, error) {
	var c DeliveryCandidate
	row, err := depdb.New(db).GetCandidate(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	}
	if err != nil {
		return DeliveryCandidate{}, err
	}
	c.CandidateID, c.TargetID, c.PlanID, c.AttemptID, c.SnapshotSealID, c.Status, c.CandidateRevision, c.ArtifactDigest, c.QualificationDigest, c.CreatedAt = row.CandidateID, row.TargetID, row.PlanID, row.AttemptID, row.SnapshotSealID, row.Status, row.CandidateRevision, row.ArtifactDigest, row.QualificationDigest, dbTime(row.CreatedAt)
	if row.QualifiedAt.Valid {
		c.QualifiedAt = row.QualifiedAt.Time.UTC()
	}
	if row.RetiredAt.Valid {
		c.RetiredAt = row.RetiredAt.Time.UTC()
	}
	if expected.TargetID != "" && (c.TargetID != expected.TargetID || c.PlanID != expected.PlanID || c.CandidateRevision != expected.CandidateRevision || c.ArtifactDigest != expected.ArtifactDigest) {
		return DeliveryCandidate{}, ErrConflict
	}
	return c, nil
}
func (r *Repository) Candidate(ctx context.Context, id string) (DeliveryCandidate, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	id, err = uuidID(id, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(contextOrBackground(ctx), db, id, CandidateInput{})
}
func (r *Repository) LoadCandidate(ctx context.Context, id string) (DeliveryCandidate, error) {
	return r.Candidate(ctx, id)
}
func (r *Repository) QualifyCandidate(ctx context.Context, candidateID, sealID, qualificationDigest string) (DeliveryCandidate, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	candidateID, err = uuidID(candidateID, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	sealID, err = uuidID(sealID, "seal id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if _, err := digest(qualificationDigest, "qualification digest"); err != nil {
		return DeliveryCandidate{}, err
	}
	c, err := loadCandidate(contextOrBackground(ctx), db, candidateID, CandidateInput{})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	s, err := loadSeal(contextOrBackground(ctx), db, sealID)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if s.CandidateID != "" && s.CandidateID != candidateID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.AttemptID != "" && c.AttemptID != s.AttemptID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.Status != "building" && c.Status != "ready" {
		if c.Status == "qualified" && c.SnapshotSealID == sealID && c.QualificationDigest == qualificationDigest {
			return c, nil
		}
		return DeliveryCandidate{}, ErrConflict
	}
	err = depdb.New(db).QualifyCandidate(contextOrBackground(ctx), depdb.QualifyCandidateParams{CandidateID: dbUUID(candidateID), SnapshotSealID: dbUUID(sealID), QualificationDigest: pgText(&qualificationDigest)})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(contextOrBackground(ctx), db, candidateID, CandidateInput{})
}

// QualifyCandidateTx is the transaction-aware qualification form. The
// caller owns the complete commit/rollback boundary.
func (r *Repository) QualifyCandidateTx(ctx context.Context, tx Tx, candidateID, sealID, qualificationDigest string) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	candidateID, err := uuidID(candidateID, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	sealID, err = uuidID(sealID, "seal id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if _, err := digest(qualificationDigest, "qualification digest"); err != nil {
		return DeliveryCandidate{}, err
	}
	c, err := loadCandidate(ctx, tx, candidateID, CandidateInput{})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	s, err := loadSeal(ctx, tx, sealID)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if s.CandidateID != "" && s.CandidateID != candidateID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.AttemptID != "" && c.AttemptID != s.AttemptID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.Status != "building" && c.Status != "ready" {
		if c.Status == "qualified" && c.SnapshotSealID == sealID && c.QualificationDigest == qualificationDigest {
			return c, nil
		}
		return DeliveryCandidate{}, ErrConflict
	}
	if err = depdb.New(tx).QualifyCandidate(ctx, depdb.QualifyCandidateParams{CandidateID: dbUUID(candidateID), SnapshotSealID: dbUUID(sealID), QualificationDigest: pgText(&qualificationDigest)}); err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(ctx, tx, candidateID, CandidateInput{})
}

// CreateGeneration binds the immutable seal and all compiler identities.
func (r *Repository) CreateGeneration(ctx context.Context, in GenerationInput) (DeliveryGeneration, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return createGeneration(contextOrBackground(ctx), db, in)
}
func createGeneration(ctx context.Context, db DBTX, in GenerationInput) (DeliveryGeneration, error) {
	id, err := uuidID(in.GenerationID, "generation id", true)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryGeneration{}, err
	}
	for n, v := range map[string]string{"plan digest": in.PlanDigest, "serving artifact digest": in.ServingArtifactDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint} {
		if _, err := digest(v, n); err != nil {
			return DeliveryGeneration{}, err
		}
	}
	if in.GenerationRevision <= 0 {
		return DeliveryGeneration{}, ErrInvalid
	}
	if in.ArtifactRoot == "" || in.ArtifactRootDigest == "" {
		return DeliveryGeneration{}, ErrInvalid
	}
	if _, err := digest(in.ArtifactRootDigest, "artifact root digest"); err != nil {
		return DeliveryGeneration{}, err
	}
	candidate, err := uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	seal, err := uuidID(in.SnapshotSealID, "seal id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	plan, err := uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	cr, err := depdb.New(db).GetCandidateStatus(ctx, dbUUID(candidate))
	cstatus, ct, cp, cs := cr.Status, cr.TargetID, cr.PlanID, cr.SnapshotSealID
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if cstatus != "qualified" || ct != target || cp != plan || cs != seal {
		return DeliveryGeneration{}, ErrNotQualified
	}
	snapshotSeal, err := loadSeal(ctx, db, seal)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	pr, err := depdb.New(db).GetPlanDigests(ctx, dbUUID(plan))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	} else if err != nil {
		return DeliveryGeneration{}, err
	}
	if snapshotSeal.ServingArtifactDigest != in.ServingArtifactDigest || snapshotSeal.ArtifactRoot != in.ArtifactRoot || snapshotSeal.ArtifactRootDigest != in.ArtifactRootDigest || snapshotSeal.CompiledGraphDigest != in.CompiledGraphDigest || snapshotSeal.CompiledConfigDigest != in.CompiledConfigDigest || snapshotSeal.SecurityDomainFingerprint != in.SecurityDomainFingerprint || snapshotSeal.PlanDigest != in.PlanDigest || pr.PlanDigest != in.PlanDigest || pr.CompiledGraphDigest != in.CompiledGraphDigest || pr.CompiledConfigDigest != in.CompiledConfigDigest || pr.SecurityDomainFingerprint != in.SecurityDomainFingerprint || pr.ArtifactDigest != in.ServingArtifactDigest {
		return DeliveryGeneration{}, fmt.Errorf("%w: generation evidence differs from seal and plan", ErrConflict)
	}
	err = depdb.New(db).InsertGeneration(ctx, depdb.InsertGenerationParams{GenerationID: dbUUID(id), TargetID: target, CandidateID: dbUUID(candidate), SnapshotSealID: dbUUID(seal), PlanID: dbUUID(plan), PlanDigest: in.PlanDigest, ArtifactRoot: in.ArtifactRoot, ArtifactRootDigest: in.ArtifactRootDigest, ServingArtifactDigest: in.ServingArtifactDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, GenerationRevision: in.GenerationRevision})
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return loadGeneration(ctx, db, id, in)
}
func loadGeneration(ctx context.Context, db DBTX, id string, expected GenerationInput) (DeliveryGeneration, error) {
	var g DeliveryGeneration
	row, err := depdb.New(db).GetGeneration(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	g.GenerationID, g.TargetID, g.CandidateID, g.SnapshotSealID, g.PlanID, g.PlanDigest, g.ArtifactRoot, g.ArtifactRootDigest, g.ServingArtifactDigest, g.CompiledGraphDigest, g.CompiledConfigDigest, g.SecurityDomainFingerprint, g.GenerationRevision, g.CreatedAt = row.GenerationID, row.TargetID, row.CandidateID, row.SnapshotSealID, row.PlanID, row.PlanDigest, row.ArtifactRoot, row.ArtifactRootDigest, row.ServingArtifactDigest, row.CompiledGraphDigest, row.CompiledConfigDigest, row.SecurityDomainFingerprint, row.GenerationRevision, dbTime(row.CreatedAt)
	if expected.TargetID != "" && (g.TargetID != expected.TargetID || g.CandidateID != expected.CandidateID || g.SnapshotSealID != expected.SnapshotSealID || g.PlanID != expected.PlanID || g.PlanDigest != expected.PlanDigest || g.ArtifactRoot != expected.ArtifactRoot || g.ArtifactRootDigest != expected.ArtifactRootDigest || g.ServingArtifactDigest != expected.ServingArtifactDigest || g.CompiledGraphDigest != expected.CompiledGraphDigest || g.CompiledConfigDigest != expected.CompiledConfigDigest || g.SecurityDomainFingerprint != expected.SecurityDomainFingerprint || g.GenerationRevision != expected.GenerationRevision) {
		return DeliveryGeneration{}, ErrConflict
	}
	return g, nil
}
func (r *Repository) Generation(ctx context.Context, id string) (DeliveryGeneration, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	id, err = uuidID(id, "generation id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return loadGeneration(contextOrBackground(ctx), db, id, GenerationInput{})
}

// GenerationTx reads immutable serving-generation evidence through a
// caller-owned transaction.
func (r *Repository) GenerationTx(ctx context.Context, tx Tx, id string) (DeliveryGeneration, error) {
	if tx == nil {
		return DeliveryGeneration{}, ErrInvalid
	}
	id, err := uuidID(id, "generation id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return loadGeneration(contextOrBackground(ctx), tx, id, GenerationInput{})
}

// TargetTx reads the immutable project/environment identity for a delivery
// target through a caller-owned transaction.
func (r *Repository) TargetTx(ctx context.Context, tx Tx, id string) (DeliveryTarget, error) {
	if tx == nil {
		return DeliveryTarget{}, ErrInvalid
	}
	id, err := textID(id, "target id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	return loadTarget(contextOrBackground(ctx), tx, id)
}

// TargetForShareTx reads and share-locks the immutable delivery target row.
// Activation acquires the same row FOR UPDATE, so a canonical refresh proof
// that uses this projection cannot be overtaken before its transaction commits.
func (r *Repository) TargetForShareTx(ctx context.Context, tx Tx, id string) (DeliveryTarget, error) {
	if tx == nil {
		return DeliveryTarget{}, ErrInvalid
	}
	id, err := textID(id, "target id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	var target DeliveryTarget
	row, err := depdb.New(tx).LockTargetForShare(contextOrBackground(ctx), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTarget{}, ErrNotFound
	}
	if err != nil {
		return target, err
	}
	target.TargetID, target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID, target.ActivePublicationID, target.CreatedAt, target.UpdatedAt = row.TargetID, row.ProjectID, row.Environment, row.TargetRevision, row.ActiveGenerationID, row.ActivePublicationID, dbTime(row.CreatedAt), dbTime(row.UpdatedAt)
	return target, nil
}
func (r *Repository) LoadGeneration(ctx context.Context, id string) (DeliveryGeneration, error) {
	return r.Generation(ctx, id)
}

// CreatePublication records a pending request. Activation is the only path
// that advances the target pointer.
func (r *Repository) CreatePublication(ctx context.Context, in PublicationInput) (DeliveryPublication, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryPublication{}, err
	}
	return createPublication(contextOrBackground(ctx), db, in)
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
	updated, err := depdb.New(db).ReleaseLease(contextOrBackground(ctx), depdb.ReleaseLeaseParams{LeaseID: dbUUID(id), TargetID: target, OwnerID: owner, FencingEpoch: f.FencingEpoch})
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
	if l.State == "released" {
		return nil
	}
	return ErrStaleFence
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

// Activate performs target lock, lease fence check, exact seal validation,
// CAS pointer advance, publication outcome, durable event and audit insertion
// in one PostgreSQL transaction. A committed exact replay is returned even if
// the original worker's lease has since expired: the prior transition itself
// is the proof of outcome.
func (r *Repository) Activate(ctx context.Context, in ActivationInput) (ActivationResult, error) {
	if r == nil || r.audit == nil {
		return ActivationResult{}, fmt.Errorf("%w: activation audit port is required", ErrInvalid)
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
	if r == nil || r.audit == nil {
		return ActivationResult{}, fmt.Errorf("%w: activation audit port is required", ErrInvalid)
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
	requiresApproval, err := depdb.New(tx).GetPlanQualification(ctx, dbUUID(p.GenerationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	} else if err != nil {
		return ActivationResult{}, err
	}
	if requiresApproval {
		approved, err := depdb.New(tx).CandidateApproved(ctx, dbUUID(p.CandidateID))
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
	event, err := appendActivationEvent(ctx, tx, p, in, newRev, actor)
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

// DeliveryResultError is a tiny helper used to keep stale-fence branches
// explicit while preserving the ordinary ActivationResult return shape.
func DeliveryResultError(err error) (ActivationResult, error) { return ActivationResult{}, err }

func activationPayload(p DeliveryPublication, revision int64) json.RawMessage {
	payload, _ := canonicalObject(json.RawMessage(fmt.Sprintf(`{"publication_id":%q,"generation_id":%q,"target_revision":%d}`, p.PublicationID, p.GenerationID, revision)), 65536, true)
	return payload
}

func activationMetadata(p DeliveryPublication) json.RawMessage {
	metadata, _ := canonicalObject(json.RawMessage(fmt.Sprintf(`{"generation_id":%q,"target_revision":%d}`, p.GenerationID, p.ResultTargetRevision)), 32768, true)
	return metadata
}

func ensureActivationRoot(ctx context.Context, tx Tx, p DeliveryPublication, target string) error {
	row, err := depdb.New(tx).LockRetentionRoot(ctx, dbUUID(p.GenerationID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = depdb.New(tx).InsertGenerationRoot(ctx, depdb.InsertGenerationRootParams{RootID: dbUUID(p.GenerationID), TargetID: target, CandidateID: dbUUID(p.CandidateID), GenerationID: dbUUID(p.GenerationID), SnapshotSealID: dbUUID(p.SnapshotSealID)})
		return err
	}
	if err != nil {
		return err
	}
	if row.TargetID != target || row.CandidateID != p.CandidateID || row.GenerationID != p.GenerationID || row.SnapshotSealID != p.SnapshotSealID || row.RootKind != "generation" || row.State != "live" {
		return fmt.Errorf("%w: activation retention root identity differs", ErrConflict)
	}
	return nil
}

func appendActivationEvent(ctx context.Context, tx Tx, p DeliveryPublication, in ActivationInput, revision int64, actor string) (Event, error) {
	payload := activationPayload(p, revision)
	e, err := eventspostgres.New().AppendEvent(ctx, tx, eventspostgres.EventInput{EventID: p.PublicationID, ScopeID: in.TargetID, AggregateType: "delivery_target", AggregateID: in.TargetID, EventType: "activation_committed", SchemaVersion: 1, CorrelationID: in.CorrelationID, Payload: payload})
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
	event, err := eventspostgres.New().GetEvent(ctx, tx, publicationID)
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
	if root.RootKind == "candidate" && root.CandidateID == "" || root.RootKind == "generation" && root.GenerationID == "" {
		return DeliveryRetentionRoot{}, ErrInvalid
	}
	if !root.ExpiresAt.IsZero() {
		root.ExpiresAt = root.ExpiresAt.UTC().Truncate(time.Microsecond)
	}
	evidence, err := canonicalObject(root.Evidence, 16384, true)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	err = depdb.New(db).InsertRetentionRoot(contextOrBackground(ctx), depdb.InsertRetentionRootParams{RootID: dbUUID(id), TargetID: target, CandidateID: dbUUID(root.CandidateID), GenerationID: dbUUID(root.GenerationID), SnapshotSealID: dbUUID(root.SnapshotSealID), RootKind: root.RootKind, State: root.State, ExpiresAt: nullablePgTime(root.ExpiresAt), Evidence: evidence})
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	persisted, err := loadRetentionRoot(contextOrBackground(ctx), db, id)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	if persisted.TargetID != target || persisted.CandidateID != root.CandidateID || persisted.GenerationID != root.GenerationID || persisted.SnapshotSealID != root.SnapshotSealID || persisted.RootKind != root.RootKind || persisted.State != root.State || !sameCanonical(persisted.Evidence, evidence) || !nullableTimesEqual(persisted.ExpiresAt, root.ExpiresAt) {
		return DeliveryRetentionRoot{}, ErrConflict
	}
	return persisted, nil
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
