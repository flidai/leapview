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
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is implemented by pgx connections, transactions and pools.  A caller
// may pass a transaction to the Tx methods to compose activation with another
// control mutation without crossing a database boundary.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Tx = DBTX

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
	SealID, AttemptID, CandidateID                                                                        string
	PhysicalPoolID, TenantDomain, Region, EncryptionDomain, ObjectNamespace, CatalogDatabase, CatalogUUID string
	CatalogVersion, DuckLakeSnapshotID                                                                    int64
	RelationNamespace, ObjectRoot, ObjectRootDigest, ArtifactRoot, ArtifactRootDigest                     string
	RelationManifestDigest                                                                                string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint                                  string
	RequestDigest, PlanDigest, CompatibilityDigest, ServingArtifactDigest                                 string
	DuckDBVersion, DuckLakeExtensionVersion, DuckLakeSpecVersion, CatalogSchemaVersion                    string
	QualificationEvidence                                                                                 json.RawMessage
	QualifiedAt                                                                                           time.Time
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
	PublicationID, TargetID, GenerationID, CandidateID, SnapshotSealID string
	ExpectedTargetRevision, ResultTargetRevision                       int64
	ActorID, State, RequestDigest                                      string
	CreatedAt, CommittedAt                                             time.Time
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
	var a DeliveryApproval
	var principal string
	var evidence []byte
	err := db.QueryRow(ctx, `SELECT approval_id::text,candidate_id::text,COALESCE(principal_id::text,''),decision,evidence,decided_at FROM delivery.delivery_approval WHERE approval_id=$1::uuid`, id).
		Scan(&a.ApprovalID, &a.CandidateID, &principal, &a.Decision, &evidence, &a.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryApproval{}, ErrNotFound
	}
	if err != nil {
		return DeliveryApproval{}, err
	}
	a.PrincipalID = principal
	a.Evidence = append([]byte(nil), evidence...)
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
	var principal any
	if strings.TrimSpace(in.PrincipalID) != "" {
		principalID, err = uuidID(in.PrincipalID, "principal id", false)
		if err != nil {
			return DeliveryApproval{}, err
		}
		principal = principalID
	}
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO delivery.delivery_approval(approval_id,candidate_id,principal_id,decision,evidence) VALUES($1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4,$5::jsonb) ON CONFLICT(approval_id) DO NOTHING`, id, candidate, principal, in.Decision, evidence)
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
	var generation, seal string
	var expires, retired, expired *time.Time
	var evidence []byte
	var candidate string
	err := db.QueryRow(ctx, `SELECT root_id::text,target_id,COALESCE(candidate_id::text,''),COALESCE(generation_id::text,''),COALESCE(snapshot_seal_id::text,''),root_kind,state,expires_at,evidence,created_at,retired_at,expired_at FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, id).
		Scan(&r.RootID, &r.TargetID, &candidate, &generation, &seal, &r.RootKind, &r.State, &expires, &evidence, &r.CreatedAt, &retired, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryRetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	r.CandidateID, r.GenerationID, r.SnapshotSealID = candidate, generation, seal
	if expires != nil {
		r.ExpiresAt = expires.UTC()
	}
	if retired != nil {
		r.RetiredAt = retired.UTC()
	}
	if expired != nil {
		r.ExpiredAt = expired.UTC()
	}
	r.Evidence = append([]byte(nil), evidence...)
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

type Repository struct{ db DBTX }

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
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository { return &Repository{db: db} }

func databaseNow(ctx context.Context, db DBTX) (time.Time, error) {
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
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
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_target(target_id,project_id,environment,target_revision)
VALUES ($1,$2,$3,$4) ON CONFLICT (target_id) DO NOTHING`, id, project, env, rev)
	if err != nil {
		return DeliveryTarget{}, err
	}
	if _, err := db.Exec(ctx, `INSERT INTO delivery.delivery_target_fence(target_id,next_fencing_epoch) VALUES($1,1) ON CONFLICT(target_id) DO NOTHING`, id); err != nil {
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
	var activeGen, activePub string
	err = db.QueryRow(ctx, `SELECT t.target_id,t.project_id,t.environment,t.target_revision,
COALESCE((SELECT generation_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),''),
COALESCE((SELECT publication_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),''),
t.created_at,t.updated_at FROM delivery.delivery_target t WHERE t.target_id=$1`, id).
		Scan(&out.TargetID, &out.ProjectID, &out.Environment, &out.TargetRevision, &activeGen, &activePub, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTarget{}, ErrNotFound
	}
	if err != nil {
		return DeliveryTarget{}, err
	}
	out.ActiveGenerationID, out.ActivePublicationID = activeGen, activePub
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
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_required,evidence)
VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb) ON CONFLICT (plan_id) DO NOTHING`, id, target, in.PlanRevision, in.PlanDigest, in.CompiledGraphDigest, in.CompiledConfigDigest, in.SecurityDomainFingerprint, in.ArtifactDigest, in.QualificationRequired, evidence)
	if err != nil {
		return DeliveryPlan{}, err
	}
	return loadPlan(ctx, db, id, in)
}
func loadPlan(ctx context.Context, db DBTX, id string, expected PlanInput) (DeliveryPlan, error) {
	var p DeliveryPlan
	var evidence []byte
	err := db.QueryRow(ctx, `SELECT plan_id::text,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_required,evidence,created_at FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, id).Scan(&p.PlanID, &p.TargetID, &p.PlanRevision, &p.PlanDigest, &p.CompiledGraphDigest, &p.CompiledConfigDigest, &p.SecurityDomainFingerprint, &p.ArtifactDigest, &p.QualificationRequired, &evidence, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p.Evidence = append([]byte(nil), evidence...)
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
	var p DeliveryPlan
	var ev []byte
	err = db.QueryRow(contextOrBackground(ctx), `SELECT plan_id::text,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_required,evidence,created_at FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, id).Scan(&p.PlanID, &p.TargetID, &p.PlanRevision, &p.PlanDigest, &p.CompiledGraphDigest, &p.CompiledConfigDigest, &p.SecurityDomainFingerprint, &p.ArtifactDigest, &p.QualificationRequired, &ev, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPlan{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPlan{}, err
	}
	p.Evidence = append([]byte(nil), ev...)
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
		if err := db.QueryRow(ctx, `SELECT plan_id::text FROM delivery.delivery_candidate WHERE candidate_id=$1::uuid`, candidate).Scan(&candidatePlan); errors.Is(err, pgx.ErrNoRows) {
			return DeliveryBuildAttempt{}, ErrNotFound
		} else if err != nil {
			return DeliveryBuildAttempt{}, err
		} else if candidatePlan != plan {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	}
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_build_attempt(attempt_id,plan_id,candidate_id,owner_id,physical_pool_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity) VALUES($1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,'running',$9,$11,$10) ON CONFLICT(attempt_id) DO NOTHING`, id, plan, candidate, owner, in.PhysicalPoolID, in.FencingEpoch, in.RequestDigest, in.PlanDigest, in.Namespace, in.SessionIdentity, lease)
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
	var state string
	var candidate string
	var marker, term []byte
	var finished *time.Time
	err := db.QueryRow(ctx, `SELECT attempt_id::text,plan_id::text,COALESCE(candidate_id::text,''),owner_id,physical_pool_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity,COALESCE(snapshot_id,0),commit_marker,termination_evidence,created_at,updated_at,finished_at FROM delivery.delivery_build_attempt WHERE attempt_id=$1::uuid`, id).Scan(&a.AttemptID, &a.PlanID, &candidate, &a.OwnerID, &a.PhysicalPoolID, &a.FencingEpoch, &a.RequestDigest, &a.PlanDigest, &state, &a.Namespace, &a.LeaseExpiresAt, &a.SessionIdentity, &a.SnapshotID, &marker, &term, &a.CreatedAt, &a.UpdatedAt, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryBuildAttempt{}, ErrNotFound
	}
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	a.CandidateID = candidate
	a.State = BuildAttemptState(state)
	a.CommitMarker = append([]byte(nil), marker...)
	a.TerminationEvidence = append([]byte(nil), term...)
	if finished != nil {
		a.FinishedAt = finished.UTC()
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
	var lockedID string
	if err := db.QueryRow(ctx, `SELECT attempt_id::text FROM delivery.delivery_build_attempt WHERE attempt_id=$1::uuid FOR UPDATE`, id).Scan(&lockedID); errors.Is(err, pgx.ErrNoRows) {
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
	if at.State != AttemptRunning {
		canonicalEvidence, evidenceErr := canonicalNonEmpty(in.CommitMarker, 32768)
		if state == AttemptCommitted {
			canonicalEvidence, evidenceErr = canonicalNonEmpty(in.CommitMarker, 4096)
		}
		if evidenceErr == nil && at.State == state && ((state == AttemptCommitted && at.SnapshotID == in.SnapshotID && sameCanonical(at.CommitMarker, canonicalEvidence)) || (state != AttemptCommitted && sameCanonical(at.TerminationEvidence, canonicalEvidence))) {
			return at, nil
		}
		return DeliveryBuildAttempt{}, ErrConflict
	}
	var leaseActive bool
	if err := db.QueryRow(ctx, `SELECT lease_expires_at > clock_timestamp() FROM delivery.delivery_build_attempt WHERE attempt_id=$1::uuid`, id).Scan(&leaseActive); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if !leaseActive {
		return DeliveryBuildAttempt{}, ErrLeaseExpired
	}
	if state == AttemptCommitted {
		if in.SnapshotID <= 0 {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		marker, err := canonicalNonEmpty(in.CommitMarker, 4096)
		if err != nil {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		if !markerMatches(marker, id, at.PhysicalPoolID, at.RequestDigest, at.PlanDigest, at.FencingEpoch) {
			return DeliveryBuildAttempt{}, fmt.Errorf("%w: commit marker identity mismatch", ErrConflict)
		}
		res, err := db.Exec(ctx, `UPDATE delivery.delivery_build_attempt SET state='committed',snapshot_id=$2,commit_marker=$3::jsonb,updated_at=clock_timestamp(),finished_at=clock_timestamp() WHERE attempt_id=$1::uuid AND state='running' AND owner_id=$4 AND fencing_epoch=$5`, id, in.SnapshotID, marker, owner, in.FencingEpoch)
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if res.RowsAffected() != 1 {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	} else {
		evidence, err := canonicalNonEmpty(in.CommitMarker, 32768)
		if err != nil {
			return DeliveryBuildAttempt{}, ErrInvalid
		}
		res, err := db.Exec(ctx, `UPDATE delivery.delivery_build_attempt SET state=$2,termination_evidence=$3::jsonb,updated_at=clock_timestamp(),finished_at=clock_timestamp() WHERE attempt_id=$1::uuid AND state='running' AND owner_id=$4 AND fencing_epoch=$5`, id, string(state), evidence, owner, in.FencingEpoch)
		if err != nil {
			return DeliveryBuildAttempt{}, err
		}
		if res.RowsAffected() != 1 {
			return DeliveryBuildAttempt{}, ErrConflict
		}
	}
	return loadAttempt(ctx, db, id)
}

type commitMarkerIdentity struct {
	AttemptID      string          `json:"attempt_id"`
	PhysicalPoolID string          `json:"physical_pool_id"`
	RequestDigest  string          `json:"request_digest"`
	PlanDigest     string          `json:"plan_digest"`
	FencingEpoch   json.RawMessage `json:"fencing_epoch"`
}

func markerMatches(raw []byte, attempt, physicalPool, request, plan string, fence int64) bool {
	var m commitMarkerIdentity
	if strictjson.DecodeWithOptions(raw, &m, strictjson.Options{AllowUnknownFields: true, MaxBytes: 4096}) != nil {
		return false
	}
	if m.AttemptID != attempt || m.PhysicalPoolID != physicalPool || m.RequestDigest != request || m.PlanDigest != plan || len(m.FencingEpoch) == 0 {
		return false
	}
	n, err := strconv.ParseInt(string(m.FencingEpoch), 10, 64)
	return err == nil && n == fence
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
	if in.PhysicalPoolID == "" || in.TenantDomain == "" || in.Region == "" || in.EncryptionDomain == "" || in.ObjectNamespace == "" || in.CatalogDatabase == "" || in.CatalogUUID == "" || in.RelationNamespace == "" || in.ObjectRoot == "" || in.ArtifactRoot == "" || in.ObjectRootDigest == "" || in.ArtifactRootDigest == "" {
		return SnapshotSeal{}, ErrInvalid
	}
	canonicalCatalogUUID, err := uuidID(in.CatalogUUID, "catalog uuid", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	in.CatalogUUID = canonicalCatalogUUID
	for label, value := range map[string]string{"physical pool id": in.PhysicalPoolID, "catalog database": in.CatalogDatabase, "relation namespace": in.RelationNamespace, "object root": in.ObjectRoot, "artifact root": in.ArtifactRoot} {
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
	for n, v := range map[string]string{"relation manifest digest": in.RelationManifestDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "request digest": in.RequestDigest, "plan digest": in.PlanDigest, "compatibility digest": in.CompatibilityDigest, "serving artifact digest": in.ServingArtifactDigest} {
		if _, err := digest(v, n); err != nil {
			return SnapshotSeal{}, err
		}
	}
	for n, v := range map[string]string{"DuckDB version": in.DuckDBVersion, "DuckLake extension version": in.DuckLakeExtensionVersion, "DuckLake specification version": in.DuckLakeSpecVersion, "catalog schema version": in.CatalogSchemaVersion} {
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
	var candidateTarget, candidatePlan, candidateStatus, candidateArtifact string
	if err := db.QueryRow(ctx, `SELECT target_id,plan_id::text,status,artifact_digest FROM delivery.delivery_candidate WHERE candidate_id=$1::uuid`, candidate).Scan(&candidateTarget, &candidatePlan, &candidateStatus, &candidateArtifact); errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	} else if err != nil {
		return SnapshotSeal{}, err
	}
	if candidateStatus == "rejected" || candidateStatus == "retired" || candidatePlan != at.PlanID || candidateArtifact != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: candidate evidence differs", ErrNotQualified)
	}
	var planGraph, planConfig, planSecurity, planArtifact, planDigest string
	if err := db.QueryRow(ctx, `SELECT plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, at.PlanID).Scan(&planDigest, &planGraph, &planConfig, &planSecurity, &planArtifact); errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	} else if err != nil {
		return SnapshotSeal{}, err
	}
	if planDigest != in.PlanDigest || planGraph != in.CompiledGraphDigest || planConfig != in.CompiledConfigDigest || planSecurity != in.SecurityDomainFingerprint || planArtifact != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: plan evidence differs", ErrNotQualified)
	}
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_snapshot_seal(seal_id,attempt_id,candidate_id,physical_pool_id,tenant_domain,region,encryption_domain,object_namespace,catalog_database,catalog_uuid,catalog_version,ducklake_snapshot_id,relation_namespace,relation_manifest_digest,object_root,object_root_digest,artifact_root,artifact_root_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,request_digest,plan_digest,compatibility_digest,serving_artifact_digest,duckdb_version,ducklake_extension_version,ducklake_spec_version,catalog_schema_version,qualification_evidence) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30::jsonb) ON CONFLICT(seal_id) DO NOTHING`, id, attempt, candidate, in.PhysicalPoolID, in.TenantDomain, in.Region, in.EncryptionDomain, in.ObjectNamespace, in.CatalogDatabase, in.CatalogUUID, in.CatalogVersion, in.DuckLakeSnapshotID, in.RelationNamespace, in.RelationManifestDigest, in.ObjectRoot, in.ObjectRootDigest, in.ArtifactRoot, in.ArtifactRootDigest, in.CompiledGraphDigest, in.CompiledConfigDigest, in.SecurityDomainFingerprint, in.RequestDigest, in.PlanDigest, in.CompatibilityDigest, in.ServingArtifactDigest, in.DuckDBVersion, in.DuckLakeExtensionVersion, in.DuckLakeSpecVersion, in.CatalogSchemaVersion, evidence)
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
	return a.AttemptID == b.AttemptID && a.CandidateID == b.CandidateID && a.PhysicalPoolID == b.PhysicalPoolID && a.TenantDomain == b.TenantDomain && a.Region == b.Region && a.EncryptionDomain == b.EncryptionDomain && a.ObjectNamespace == b.ObjectNamespace && a.CatalogDatabase == b.CatalogDatabase && a.CatalogUUID == b.CatalogUUID && a.CatalogVersion == b.CatalogVersion && a.DuckLakeSnapshotID == b.DuckLakeSnapshotID && a.RelationNamespace == b.RelationNamespace && a.RelationManifestDigest == b.RelationManifestDigest && a.ObjectRoot == b.ObjectRoot && a.ObjectRootDigest == b.ObjectRootDigest && a.ArtifactRoot == b.ArtifactRoot && a.ArtifactRootDigest == b.ArtifactRootDigest && a.CompiledGraphDigest == b.CompiledGraphDigest && a.CompiledConfigDigest == b.CompiledConfigDigest && a.SecurityDomainFingerprint == b.SecurityDomainFingerprint && a.RequestDigest == b.RequestDigest && a.PlanDigest == b.PlanDigest && a.CompatibilityDigest == b.CompatibilityDigest && a.ServingArtifactDigest == b.ServingArtifactDigest && a.DuckDBVersion == b.DuckDBVersion && a.DuckLakeExtensionVersion == b.DuckLakeExtensionVersion && a.DuckLakeSpecVersion == b.DuckLakeSpecVersion && a.CatalogSchemaVersion == b.CatalogSchemaVersion && sameCanonical(a.QualificationEvidence, b.QualificationEvidence)
}

func sameCanonical(a, b []byte) bool {
	aa, err1 := canonicalObject(a, maxEvidence, true)
	bb, err2 := canonicalObject(b, maxEvidence, true)
	return err1 == nil && err2 == nil && bytes.Equal(aa, bb)
}
func loadSeal(ctx context.Context, db DBTX, id string) (SnapshotSeal, error) {
	var s SnapshotSeal
	var aid, cid string
	var evidence []byte
	err := db.QueryRow(ctx, `SELECT seal_id::text,attempt_id::text,COALESCE(candidate_id::text,''),physical_pool_id,tenant_domain,region,encryption_domain,object_namespace,catalog_database,catalog_uuid,catalog_version,ducklake_snapshot_id,relation_namespace,relation_manifest_digest,object_root,object_root_digest,artifact_root,artifact_root_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,request_digest,plan_digest,compatibility_digest,serving_artifact_digest,duckdb_version,ducklake_extension_version,ducklake_spec_version,catalog_schema_version,qualification_evidence,qualified_at FROM delivery.delivery_snapshot_seal WHERE seal_id=$1::uuid`, id).Scan(&s.SealID, &aid, &cid, &s.PhysicalPoolID, &s.TenantDomain, &s.Region, &s.EncryptionDomain, &s.ObjectNamespace, &s.CatalogDatabase, &s.CatalogUUID, &s.CatalogVersion, &s.DuckLakeSnapshotID, &s.RelationNamespace, &s.RelationManifestDigest, &s.ObjectRoot, &s.ObjectRootDigest, &s.ArtifactRoot, &s.ArtifactRootDigest, &s.CompiledGraphDigest, &s.CompiledConfigDigest, &s.SecurityDomainFingerprint, &s.RequestDigest, &s.PlanDigest, &s.CompatibilityDigest, &s.ServingArtifactDigest, &s.DuckDBVersion, &s.DuckLakeExtensionVersion, &s.DuckLakeSpecVersion, &s.CatalogSchemaVersion, &evidence, &s.QualifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	}
	if err != nil {
		return SnapshotSeal{}, err
	}
	s.AttemptID, s.CandidateID = aid, cid
	s.QualificationEvidence = append([]byte(nil), evidence...)
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
	var planTarget string
	if err := db.QueryRow(ctx, `SELECT target_id FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, plan).Scan(&planTarget); errors.Is(err, pgx.ErrNoRows) {
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
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,snapshot_seal_id,status,candidate_revision,artifact_digest,qualification_digest) VALUES($1::uuid,$2,$3::uuid,NULLIF($4,'')::uuid,$5,$6,$7,NULLIF($8,'')) ON CONFLICT(candidate_id) DO NOTHING`, id, target, plan, in.SnapshotSealID, status, in.CandidateRevision, in.ArtifactDigest, in.QualificationDigest)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(ctx, db, id, in)
}
func loadCandidate(ctx context.Context, db DBTX, id string, expected CandidateInput) (DeliveryCandidate, error) {
	var c DeliveryCandidate
	var aid, sid string
	var q, ret *time.Time
	err := db.QueryRow(ctx, `SELECT c.candidate_id::text,c.target_id,c.plan_id::text,
COALESCE((SELECT s.attempt_id::text FROM delivery.delivery_snapshot_seal s WHERE s.seal_id=c.snapshot_seal_id),''),
COALESCE(c.snapshot_seal_id::text,''),c.status,c.candidate_revision,c.artifact_digest,COALESCE(c.qualification_digest,''),c.created_at,c.qualified_at,c.retired_at FROM delivery.delivery_candidate c WHERE c.candidate_id=$1::uuid`, id).Scan(&c.CandidateID, &c.TargetID, &c.PlanID, &aid, &sid, &c.Status, &c.CandidateRevision, &c.ArtifactDigest, &c.QualificationDigest, &c.CreatedAt, &q, &ret)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	}
	if err != nil {
		return DeliveryCandidate{}, err
	}
	c.AttemptID, c.SnapshotSealID = aid, sid
	if q != nil {
		c.QualifiedAt = q.UTC()
	}
	if ret != nil {
		c.RetiredAt = ret.UTC()
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
	_, err = db.Exec(contextOrBackground(ctx), `UPDATE delivery.delivery_candidate SET status='qualified',snapshot_seal_id=$2::uuid,qualification_digest=$3,qualified_at=clock_timestamp() WHERE candidate_id=$1::uuid AND status IN ('building','ready')`, candidateID, sealID, qualificationDigest)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(contextOrBackground(ctx), db, candidateID, CandidateInput{})
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
	var cstatus string
	var ct, cs, cp string
	err = db.QueryRow(ctx, `SELECT status,target_id,plan_id::text,COALESCE(snapshot_seal_id::text,'') FROM delivery.delivery_candidate WHERE candidate_id=$1::uuid`, candidate).Scan(&cstatus, &ct, &cp, &cs)
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
	var planGraph, planConfig, planSecurity, planArtifact, storedPlanDigest string
	if err := db.QueryRow(ctx, `SELECT plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, plan).Scan(&storedPlanDigest, &planGraph, &planConfig, &planSecurity, &planArtifact); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	} else if err != nil {
		return DeliveryGeneration{}, err
	}
	if snapshotSeal.ServingArtifactDigest != in.ServingArtifactDigest || snapshotSeal.ArtifactRoot != in.ArtifactRoot || snapshotSeal.ArtifactRootDigest != in.ArtifactRootDigest || snapshotSeal.CompiledGraphDigest != in.CompiledGraphDigest || snapshotSeal.CompiledConfigDigest != in.CompiledConfigDigest || snapshotSeal.SecurityDomainFingerprint != in.SecurityDomainFingerprint || snapshotSeal.PlanDigest != in.PlanDigest || storedPlanDigest != in.PlanDigest || planGraph != in.CompiledGraphDigest || planConfig != in.CompiledConfigDigest || planSecurity != in.SecurityDomainFingerprint || planArtifact != in.ServingArtifactDigest {
		return DeliveryGeneration{}, fmt.Errorf("%w: generation evidence differs from seal and plan", ErrConflict)
	}
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_generation(generation_id,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision) VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(generation_id) DO NOTHING`, id, target, candidate, seal, plan, in.PlanDigest, in.ArtifactRoot, in.ArtifactRootDigest, in.ServingArtifactDigest, in.CompiledGraphDigest, in.CompiledConfigDigest, in.SecurityDomainFingerprint, in.GenerationRevision)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return loadGeneration(ctx, db, id, in)
}
func loadGeneration(ctx context.Context, db DBTX, id string, expected GenerationInput) (DeliveryGeneration, error) {
	var g DeliveryGeneration
	err := db.QueryRow(ctx, `SELECT generation_id::text,target_id,candidate_id::text,snapshot_seal_id::text,plan_id::text,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision,created_at FROM delivery.delivery_generation WHERE generation_id=$1::uuid`, id).Scan(&g.GenerationID, &g.TargetID, &g.CandidateID, &g.SnapshotSealID, &g.PlanID, &g.PlanDigest, &g.ArtifactRoot, &g.ArtifactRootDigest, &g.ServingArtifactDigest, &g.CompiledGraphDigest, &g.CompiledConfigDigest, &g.SecurityDomainFingerprint, &g.GenerationRevision, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
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
	if _, err := digest(in.RequestDigest, "request digest"); err != nil {
		return DeliveryPublication{}, err
	}
	actor, err := textID(in.ActorID, "actor id")
	if err != nil {
		return DeliveryPublication{}, err
	}
	var gt, gc, gs string
	if err := db.QueryRow(ctx, `SELECT target_id,candidate_id::text,snapshot_seal_id::text FROM delivery.delivery_generation WHERE generation_id=$1::uuid`, generation).Scan(&gt, &gc, &gs); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	if gt != target || gc != candidate || gs != seal {
		return DeliveryPublication{}, fmt.Errorf("%w: publication generation identity differs", ErrConflict)
	}
	var candidateStatus, candidateTarget, candidateSeal string
	if err := db.QueryRow(ctx, `SELECT status,target_id,COALESCE(snapshot_seal_id::text,'') FROM delivery.delivery_candidate WHERE candidate_id=$1::uuid`, candidate).Scan(&candidateStatus, &candidateTarget, &candidateSeal); errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	} else if err != nil {
		return DeliveryPublication{}, err
	}
	if (candidateStatus != "qualified" && candidateStatus != "ready" && candidateStatus != "admitted") || candidateTarget != target || candidateSeal != seal {
		return DeliveryPublication{}, ErrNotQualified
	}
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_publication(publication_id,target_id,generation_id,candidate_id,snapshot_seal_id,expected_target_revision,actor_id,state,request_digest) VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6,$7,'pending',$8) ON CONFLICT(publication_id) DO NOTHING`, id, target, generation, candidate, seal, in.ExpectedTargetRevision, actor, in.RequestDigest)
	if err != nil {
		return DeliveryPublication{}, err
	}
	p, err := loadPublication(ctx, db, id)
	if err != nil {
		return DeliveryPublication{}, err
	}
	if p.TargetID != target || p.GenerationID != generation || p.CandidateID != candidate || p.SnapshotSealID != seal || p.ExpectedTargetRevision != in.ExpectedTargetRevision || p.ActorID != actor || p.RequestDigest != in.RequestDigest {
		return DeliveryPublication{}, ErrConflict
	}
	return p, nil
}
func loadPublication(ctx context.Context, db DBTX, id string) (DeliveryPublication, error) {
	var p DeliveryPublication
	var committed *time.Time
	err := db.QueryRow(ctx, `SELECT publication_id::text,target_id,generation_id::text,candidate_id::text,snapshot_seal_id::text,expected_target_revision,COALESCE(result_target_revision,0),actor_id,state,request_digest,created_at,committed_at FROM delivery.delivery_publication WHERE publication_id=$1::uuid`, id).Scan(&p.PublicationID, &p.TargetID, &p.GenerationID, &p.CandidateID, &p.SnapshotSealID, &p.ExpectedTargetRevision, &p.ResultTargetRevision, &p.ActorID, &p.State, &p.RequestDigest, &p.CreatedAt, &committed)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	}
	if committed != nil {
		p.CommittedAt = committed.UTC()
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
func (r *Repository) LoadPublication(ctx context.Context, id string) (DeliveryPublication, error) {
	return r.Publication(ctx, id)
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
	if _, err := db.Exec(ctx, `INSERT INTO delivery.delivery_target_fence(target_id,next_fencing_epoch) SELECT target_id,1 FROM delivery.delivery_target WHERE target_id=$1 ON CONFLICT(target_id) DO NOTHING`, target); err != nil {
		return DeliveryLease{}, err
	}
	if err := db.QueryRow(ctx, `SELECT next_fencing_epoch FROM delivery.delivery_target_fence WHERE target_id=$1 FOR UPDATE`, target).Scan(&epoch); errors.Is(err, pgx.ErrNoRows) {
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
	var released *time.Time
	err = db.QueryRow(ctx, `SELECT lease_id::text,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at FROM delivery.delivery_lease WHERE lease_id=$1::uuid FOR UPDATE`, id).
		Scan(&existing.LeaseID, &existing.TargetID, &existing.OwnerID, &existing.FencingEpoch, &existing.State, &existing.ExpiresAt, &existing.AcquiredAt, &released)
	if err == nil {
		if released != nil {
			existing.ReleasedAt = released.UTC()
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
	if _, err := db.Exec(ctx, `UPDATE delivery.delivery_lease SET state='expired',released_at=clock_timestamp() WHERE target_id=$1 AND state='active'`, target); err != nil {
		return DeliveryLease{}, err
	}
	if _, err := db.Exec(ctx, `UPDATE delivery.delivery_target_fence SET next_fencing_epoch=$2 WHERE target_id=$1`, target, epoch+1); err != nil {
		return DeliveryLease{}, err
	}
	_, err = db.Exec(ctx, `INSERT INTO delivery.delivery_lease(lease_id,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at) VALUES($1::uuid,$2,$3,$4,'active',$5,$6) ON CONFLICT(lease_id) DO NOTHING`, id, target, owner, epoch, exp, acq)
	if err != nil {
		return DeliveryLease{}, err
	}
	return loadLease(ctx, db, id, in, epoch)
}
func loadLease(ctx context.Context, db DBTX, id string, in LeaseInput, epoch int64) (DeliveryLease, error) {
	var l DeliveryLease
	var rel *time.Time
	err := db.QueryRow(ctx, `SELECT lease_id::text,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at FROM delivery.delivery_lease WHERE lease_id=$1::uuid`, id).Scan(&l.LeaseID, &l.TargetID, &l.OwnerID, &l.FencingEpoch, &l.State, &l.ExpiresAt, &l.AcquiredAt, &rel)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, ErrNotFound
	}
	if err != nil {
		return DeliveryLease{}, err
	}
	if rel != nil {
		l.ReleasedAt = rel.UTC()
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
	var rel *time.Time
	err := db.QueryRow(ctx, `SELECT lease_id::text,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at FROM delivery.delivery_lease WHERE lease_id=$1::uuid`, id).Scan(&l.LeaseID, &l.TargetID, &l.OwnerID, &l.FencingEpoch, &l.State, &l.ExpiresAt, &l.AcquiredAt, &rel)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryLease{}, ErrNotFound
	}
	if err != nil {
		return DeliveryLease{}, err
	}
	if rel != nil {
		l.ReleasedAt = rel.UTC()
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
	res, err := db.Exec(contextOrBackground(ctx), `UPDATE delivery.delivery_lease SET state='released',released_at=clock_timestamp() WHERE lease_id=$1::uuid AND target_id=$2 AND owner_id=$3 AND fencing_epoch=$4 AND state='active' AND expires_at>clock_timestamp()`, id, target, owner, f.FencingEpoch)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 1 {
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
	res, err := db.Exec(contextOrBackground(ctx), `UPDATE delivery.delivery_lease SET expires_at=$5 WHERE lease_id=$1::uuid AND target_id=$2 AND owner_id=$3 AND fencing_epoch=$4 AND state='active' AND expires_at>clock_timestamp()`, id, target, owner, f.FencingEpoch, exp)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 1 {
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
	_, pubErr = tx.Exec(ctx, `SELECT publication_id FROM delivery.delivery_publication WHERE publication_id=$1::uuid FOR UPDATE`, pid)
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
		event, audit, err := loadActivationEvidence(ctx, tx, pid)
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
	var leaseOwner, leaseTarget, leaseState string
	var leaseEpoch int64
	var leaseActive bool
	err = tx.QueryRow(ctx, `SELECT target_id,owner_id,fencing_epoch,state,expires_at > clock_timestamp() FROM delivery.delivery_lease WHERE lease_id=$1::uuid FOR UPDATE`, in.LeaseID).Scan(&leaseTarget, &leaseOwner, &leaseEpoch, &leaseState, &leaseActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrStaleFence
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if leaseTarget != target || leaseOwner != in.OwnerID || leaseEpoch != in.FencingEpoch || leaseState != "active" {
		return ActivationResult{}, ErrStaleFence
	}
	if !leaseActive {
		return ActivationResult{}, ErrLeaseExpired
	}
	// Lock target and verify CAS revision.
	var currentRev int64
	err = tx.QueryRow(ctx, `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE`, target).Scan(&currentRev)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if currentRev != in.ExpectedTargetRevision {
		return ActivationResult{}, ErrCASConflict
	}
	var cstatus, ct, cs string
	err = tx.QueryRow(ctx, `SELECT status,target_id,COALESCE(snapshot_seal_id::text,'') FROM delivery.delivery_candidate WHERE candidate_id=$1::uuid`, p.CandidateID).Scan(&cstatus, &ct, &cs)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if cstatus != "qualified" && cstatus != "ready" && cstatus != "admitted" || ct != target || cs != p.SnapshotSealID {
		return ActivationResult{}, ErrNotQualified
	}
	var sealAttempt, sealReq, sealPlan string
	var snap int64
	err = tx.QueryRow(ctx, `SELECT attempt_id::text,request_digest,plan_digest,ducklake_snapshot_id FROM delivery.delivery_snapshot_seal WHERE seal_id=$1::uuid`, p.SnapshotSealID).Scan(&sealAttempt, &sealReq, &sealPlan, &snap)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	var genTarget, genCand, genSeal string
	err = tx.QueryRow(ctx, `SELECT target_id,candidate_id::text,snapshot_seal_id::text FROM delivery.delivery_generation WHERE generation_id=$1::uuid`, p.GenerationID).Scan(&genTarget, &genCand, &genSeal)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	}
	if err != nil {
		return ActivationResult{}, err
	}
	if genTarget != target || genCand != p.CandidateID || genSeal != p.SnapshotSealID {
		return ActivationResult{}, fmt.Errorf("%w: generation identity differs", ErrConflict)
	}
	var requiresApproval bool
	if err := tx.QueryRow(ctx, `SELECT qualification_required FROM delivery.delivery_plan WHERE plan_id=(SELECT plan_id FROM delivery.delivery_generation WHERE generation_id=$1::uuid)`, p.GenerationID).Scan(&requiresApproval); errors.Is(err, pgx.ErrNoRows) {
		return ActivationResult{}, ErrNotFound
	} else if err != nil {
		return ActivationResult{}, err
	}
	if requiresApproval {
		var approved bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT decision='approved' FROM delivery.delivery_approval WHERE candidate_id=$1::uuid ORDER BY decided_at DESC, approval_id DESC LIMIT 1),false)`, p.CandidateID).Scan(&approved); err != nil {
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
	targetUpdate, err := tx.Exec(ctx, `UPDATE delivery.delivery_target SET target_revision=$2,updated_at=clock_timestamp() WHERE target_id=$1 AND target_revision=$3`, target, newRev, currentRev)
	if err != nil {
		return ActivationResult{}, err
	}
	if targetUpdate.RowsAffected() != 1 {
		return ActivationResult{}, ErrCASConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id) VALUES($1,$2::uuid,$3::uuid) ON CONFLICT(target_id) DO UPDATE SET generation_id=EXCLUDED.generation_id,publication_id=EXCLUDED.publication_id,changed_at=clock_timestamp()`, target, p.GenerationID, p.PublicationID)
	if err != nil {
		return ActivationResult{}, err
	}
	publicationUpdate, err := tx.Exec(ctx, `UPDATE delivery.delivery_publication SET state='committed',result_target_revision=$2,committed_at=clock_timestamp() WHERE publication_id=$1::uuid AND state='pending'`, p.PublicationID, newRev)
	if err != nil {
		return ActivationResult{}, err
	}
	if publicationUpdate.RowsAffected() != 1 {
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
	audit, err := appendAudit(ctx, tx, p, event, actor)
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
	var existingTarget, existingCandidate, existingGeneration, existingSeal, kind, state string
	err := tx.QueryRow(ctx, `SELECT target_id,COALESCE(candidate_id::text,''),COALESCE(generation_id::text,''),COALESCE(snapshot_seal_id::text,''),root_kind,state FROM delivery.delivery_retention_root WHERE root_id=$1::uuid FOR UPDATE`, p.GenerationID).
		Scan(&existingTarget, &existingCandidate, &existingGeneration, &existingSeal, &kind, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO delivery.delivery_retention_root(root_id,target_id,candidate_id,generation_id,snapshot_seal_id,root_kind,state) VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,'generation','live')`, p.GenerationID, target, p.CandidateID, p.GenerationID, p.SnapshotSealID)
		return err
	}
	if err != nil {
		return err
	}
	if existingTarget != target || existingCandidate != p.CandidateID || existingGeneration != p.GenerationID || existingSeal != p.SnapshotSealID || kind != "generation" || state != "live" {
		return fmt.Errorf("%w: activation retention root identity differs", ErrConflict)
	}
	return nil
}

func appendActivationEvent(ctx context.Context, tx Tx, p DeliveryPublication, in ActivationInput, revision int64, actor string) (Event, error) {
	var version int64
	if _, err := tx.Exec(ctx, `INSERT INTO event.event_aggregate(scope_id,aggregate_type,aggregate_id,next_version) VALUES($1,'delivery_target',$1,1) ON CONFLICT(scope_id,aggregate_type,aggregate_id) DO NOTHING`, in.TargetID); err != nil {
		return Event{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE event.event_aggregate SET next_version=next_version+1 WHERE scope_id=$1 AND aggregate_type='delivery_target' AND aggregate_id=$1 RETURNING next_version-1`, in.TargetID).Scan(&version); err != nil {
		return Event{}, err
	}
	if version == 0 {
		version = 1
	}
	payload := activationPayload(p, revision)
	eventID := p.PublicationID
	if _, err := tx.Exec(ctx, `INSERT INTO event.event_log(event_id,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,occurred_at,correlation_id,payload) VALUES($1::uuid,$2,'delivery_target',$2,$3,'activation_committed',1,clock_timestamp(),NULLIF($4,'')::uuid,$5::jsonb) ON CONFLICT(event_id) DO NOTHING`, eventID, in.TargetID, version, in.CorrelationID, payload); err != nil {
		return Event{}, err
	}
	var e Event
	var corr string
	var pl []byte
	err := tx.QueryRow(ctx, `SELECT event_id::text,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,occurred_at,COALESCE(correlation_id::text,''),payload FROM event.event_log WHERE event_id=$1::uuid`, eventID).Scan(&e.EventID, &e.ScopeID, &e.AggregateType, &e.AggregateID, &e.AggregateVersion, &e.EventType, &e.SchemaVersion, &e.OccurredAt, &corr, &pl)
	if err != nil {
		return Event{}, err
	}
	e.CorrelationID = corr
	e.Payload = append([]byte(nil), pl...)
	_ = actor
	if e.EventID != p.PublicationID || e.ScopeID != in.TargetID || e.AggregateType != "delivery_target" || e.AggregateID != in.TargetID || e.AggregateVersion != version || e.EventType != "activation_committed" || e.SchemaVersion != 1 || e.CorrelationID != in.CorrelationID || !sameCanonical(e.Payload, payload) {
		return Event{}, fmt.Errorf("%w: activation event identity differs", ErrConflict)
	}
	return e, nil
}
func appendAudit(ctx context.Context, tx Tx, p DeliveryPublication, e Event, actor string) (AuditEvent, error) {
	metadata := activationMetadata(p)
	if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_event(audit_id,event_id,scope_id,actor_id,action,resource_kind,resource_id,outcome,request_digest,metadata) VALUES($1::uuid,$2::uuid,$3,$4,'activate','generation',$5,'accepted',$6,$7::jsonb) ON CONFLICT(audit_id) DO NOTHING`, p.PublicationID, e.EventID, e.ScopeID, actor, p.GenerationID, p.RequestDigest, metadata); err != nil {
		return AuditEvent{}, err
	}
	var a AuditEvent
	var md []byte
	err := tx.QueryRow(ctx, `SELECT audit_id::text,COALESCE(event_id::text,''),scope_id,actor_id,action,resource_kind,resource_id,outcome,COALESCE(request_digest,''),metadata,occurred_at FROM audit.audit_event WHERE audit_id=$1::uuid`, p.PublicationID).Scan(&a.AuditID, &a.EventID, &a.ScopeID, &a.ActorID, &a.Action, &a.ResourceKind, &a.ResourceID, &a.Outcome, &a.RequestDigest, &md, &a.OccurredAt)
	a.Metadata = append([]byte(nil), md...)
	if err != nil {
		return AuditEvent{}, err
	}
	if a.AuditID != p.PublicationID || a.EventID != e.EventID || a.ScopeID != e.ScopeID || a.ActorID != actor || a.Action != "activate" || a.ResourceKind != "generation" || a.ResourceID != p.GenerationID || a.Outcome != "accepted" || a.RequestDigest != p.RequestDigest || !sameCanonical(a.Metadata, metadata) {
		return AuditEvent{}, fmt.Errorf("%w: activation audit identity differs", ErrConflict)
	}
	return a, nil
}
func loadActivationEvidence(ctx context.Context, tx Tx, publicationID string) (Event, AuditEvent, error) {
	var e Event
	var corr string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT event_id::text,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,occurred_at,COALESCE(correlation_id::text,''),payload FROM event.event_log WHERE event_id=$1::uuid`, publicationID).Scan(&e.EventID, &e.ScopeID, &e.AggregateType, &e.AggregateID, &e.AggregateVersion, &e.EventType, &e.SchemaVersion, &e.OccurredAt, &corr, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, AuditEvent{}, fmt.Errorf("%w: activation event evidence missing", ErrConflict)
	}
	if err != nil {
		return Event{}, AuditEvent{}, err
	}
	e.CorrelationID = corr
	e.Payload = append([]byte(nil), payload...)
	var a AuditEvent
	var md []byte
	err = tx.QueryRow(ctx, `SELECT audit_id::text,COALESCE(event_id::text,''),scope_id,actor_id,action,resource_kind,resource_id,outcome,COALESCE(request_digest,''),metadata,occurred_at FROM audit.audit_event WHERE audit_id=$1::uuid`, publicationID).Scan(&a.AuditID, &a.EventID, &a.ScopeID, &a.ActorID, &a.Action, &a.ResourceKind, &a.ResourceID, &a.Outcome, &a.RequestDigest, &md, &a.OccurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, AuditEvent{}, fmt.Errorf("%w: activation audit evidence missing", ErrConflict)
	}
	a.Metadata = append([]byte(nil), md...)
	return e, a, err
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
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO delivery.delivery_retention_root(root_id,target_id,candidate_id,generation_id,snapshot_seal_id,root_kind,state,expires_at,evidence) VALUES($1::uuid,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,$8,$9::jsonb) ON CONFLICT(root_id) DO NOTHING`, id, target, root.CandidateID, root.GenerationID, root.SnapshotSealID, root.RootKind, root.State, nullableTime(root.ExpiresAt), evidence)
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
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
