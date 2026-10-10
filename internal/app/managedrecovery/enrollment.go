package managedrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ManagedEnrollmentRequest struct {
	InstanceHome      string    `json:"instanceHome"`
	RecoverySetID     string    `json:"recoverySetId"`
	FrontierDigest    string    `json:"frontierDigest"`
	RetentionRootID   string    `json:"retentionRootId"`
	SourceSystemID    string    `json:"sourceSystemId"`
	AuthoritySystemID string    `json:"authoritySystemId"`
	ArtifactIdentity  string    `json:"artifactIdentity"`
	Actor             string    `json:"actor"`
	PlannedAt         time.Time `json:"plannedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

// Enrollment is explicit operator authority to copy an already prepared,
// retained frontier. It never imports validation success or caller-built rows.
// The source home must remain exclusively owned for capture and enrollment.
type ManagedEnrollmentReceipt struct {
	SchemaVersion      int                      `json:"schemaVersion"`
	Request            ManagedEnrollmentRequest `json:"request"`
	CanonicalSetSHA256 string                   `json:"canonicalSetSha256"`
	OccurrenceID       string                   `json:"occurrenceId"`
	PolicySHA256       string                   `json:"policySha256"`
	Status             string                   `json:"status"`
}

func EnrollManagedRecovery(ctx context.Context, source, authority *pgxpool.Pool, request ManagedEnrollmentRequest) (ManagedEnrollmentReceipt, error) {
	if source == nil || authority == nil || !filepath.IsAbs(request.InstanceHome) || filepath.Clean(request.InstanceHome) != request.InstanceHome || request.InstanceHome == "/" || strings.TrimSpace(request.Actor) != request.Actor || len(request.Actor) > 256 || strings.ContainsAny(request.Actor, "\r\n\x00") || request.PlannedAt.Nanosecond()%1000 != 0 || request.ExpiresAt.Nanosecond()%1000 != 0 || !request.ExpiresAt.After(time.Now()) || request.RecoverySetID == "" || request.RetentionRootID == "" || !validContentDigest(request.FrontierDigest) || request.SourceSystemID == "" || request.AuthoritySystemID == "" || request.SourceSystemID == request.AuthoritySystemID || request.Actor == "" || request.PlannedAt.IsZero() || !request.ExpiresAt.After(request.PlannedAt) {
		return ManagedEnrollmentReceipt{}, errors.New("exact source, independent authority and retained recovery enrollment required")
	}
	if err := recovery.ValidateArtifactIdentity(request.ArtifactIdentity); err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	// Hold the source retention row until the destination transaction commits;
	// a concurrent retire/expire cannot invalidate the source proof mid-copy.
	sourceTx, err := source.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ManagedEnrollmentReceipt{}, errors.New("source recovery authority unavailable")
	}
	defer sourceTx.Rollback(context.Background())
	var sourceID string
	if err := sourceTx.QueryRow(ctx, "SELECT system_identifier::text FROM pg_control_system()").Scan(&sourceID); err != nil || sourceID != request.SourceSystemID {
		return ManagedEnrollmentReceipt{}, errors.New("source PostgreSQL system identity differs")
	}
	set, err := recoverypostgres.New(sourceTx).ReadExact(ctx, request.RecoverySetID)
	if err != nil {
		return ManagedEnrollmentReceipt{}, errors.New("exact prepared source recovery frontier unavailable")
	}
	frontier, err := set.Digest()
	if err != nil || set.Status != recoveryset.StatusPrepared || set.PublishedValidationAttemptID != "" || set.FrontierDigest != request.FrontierDigest || frontier != request.FrontierDigest {
		return ManagedEnrollmentReceipt{}, errors.New("source frontier differs or already contains qualification authority")
	}
	for _, point := range set.ClusterPoints {
		if point.ClusterIdentity != "postgres-system-id:"+sourceID {
			return ManagedEnrollmentReceipt{}, errors.New("source database does not own the selected managed cluster")
		}
	}
	var expires time.Time
	// sqlc-exception:managed-recovery-enrollment -- source-only exact retention
	// proof spans two independent databases; capability repositories own writes.
	err = sourceTx.QueryRow(ctx, `SELECT expires_at FROM delivery.delivery_retention_root
WHERE root_id=$1::uuid AND target_id=$2 AND generation_id=$3::uuid
AND snapshot_seal_id=$4::uuid AND root_kind='recovery' AND state='live'
AND evidence->>'recovery_set_id'=$5 AND evidence->>'frontier_digest'=$6
AND expires_at>clock_timestamp() FOR SHARE`, request.RetentionRootID, set.Delivery.TargetID, set.Delivery.GenerationID, set.Serving.SealID, set.ID, set.FrontierDigest).Scan(&expires)
	if err != nil || expires.Before(request.ExpiresAt) {
		return ManagedEnrollmentReceipt{}, errors.New("live source retention does not cover the requested recovery interval")
	}
	canonical, err := set.CanonicalJSON()
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	policy, err := managedEnrollmentPolicy(request, canonical)
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	intent := managedEnrollmentIntent(request, set, policy)
	if err := intent.Validate(); err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	tx, err := authority.Begin(ctx)
	if err != nil {
		return ManagedEnrollmentReceipt{}, errors.New("independent recovery authority unavailable")
	}
	defer tx.Rollback(context.Background())
	var authorityID string
	if err := tx.QueryRow(ctx, "SELECT system_identifier::text FROM pg_control_system()").Scan(&authorityID); err != nil || authorityID != request.AuthoritySystemID || authorityID == sourceID {
		return ManagedEnrollmentReceipt{}, errors.New("independent PostgreSQL authority identity differs")
	}
	retained, err := recoverypostgres.New(tx).CreateTx(ctx, tx, set)
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	retainedJSON, err := retained.CanonicalJSON()
	if err != nil || !bytes.Equal(canonical, retainedJSON) {
		return ManagedEnrollmentReceipt{}, errors.New("independent recovery frontier differs from exact source bytes")
	}
	// pgx transactions implement nested Begin as savepoints. Both the immutable
	// set and pending restore intent are committed by this one outer transaction.
	occurrence, _, err := refreshpostgres.NewRecoveryLedger(tx).Enqueue(ctx, intent, time.Now().UTC())
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ManagedEnrollmentReceipt{}, errors.New("managed recovery enrollment commit unconfirmed; retry exact input")
	}
	return ManagedEnrollmentReceipt{SchemaVersion: 1, Request: request, CanonicalSetSHA256: digestBytes(canonical), OccurrenceID: occurrence.ID, PolicySHA256: policy, Status: "prepared"}, nil
}

func managedEnrollmentPolicy(request ManagedEnrollmentRequest, canonical []byte) (string, error) {
	value, err := json.Marshal(struct {
		Kind               string
		Request            ManagedEnrollmentRequest
		CanonicalSetSHA256 string
	}{"leapview-managed-recovery-enrollment-v1", request, digestBytes(canonical)})
	if err != nil {
		return "", err
	}
	return digestBytes(value)[7:], nil
}

func managedEnrollmentIntent(request ManagedEnrollmentRequest, set recoveryset.RecoverySet, policy string) recovery.EnqueueInput {
	return recovery.EnqueueInput{ScheduleID: "managed-restore-" + set.ID, Scenario: "managed-local-restore", Operation: recovery.OperationRestore, PolicyVersion: "managed-enrollment-v1", PolicySHA256: policy, TargetScope: set.Delivery.TargetID, ArtifactIdentity: request.ArtifactIdentity, PlannedAt: request.PlannedAt, StaleAfter: request.ExpiresAt.Sub(request.PlannedAt)}
}

// VerifyManagedEnrollment binds private operator input to the immutable intent
// in the independent authority. A modified receipt cannot authorize another
// instance lock, frontier, image, interval or pending occurrence.
func VerifyManagedEnrollment(receipt ManagedEnrollmentReceipt, set recoveryset.RecoverySet, occurrence recovery.Occurrence, instanceHome string) error {
	if err := set.Validate(); err != nil {
		return err
	}
	// Publication may commit before the restoring process returns. Preserve
	// exact enrollment identity across that retry without changing the durable
	// record or claiming that enrollment itself qualified the frontier.
	set.Status, set.PublishedValidationAttemptID = recoveryset.StatusPrepared, ""
	canonical, err := set.CanonicalJSON()
	if err != nil {
		return err
	}
	request := receipt.Request
	if receipt.SchemaVersion != 1 || receipt.Status != "prepared" || request.InstanceHome != instanceHome || !filepath.IsAbs(instanceHome) || filepath.Clean(instanceHome) != instanceHome || instanceHome == "/" || request.RecoverySetID != set.ID || request.FrontierDigest != set.FrontierDigest || receipt.CanonicalSetSHA256 != digestBytes(canonical) {
		return errors.New("managed enrollment differs from exact instance and retained frontier")
	}
	policy, err := managedEnrollmentPolicy(request, canonical)
	if err != nil {
		return err
	}
	intent := managedEnrollmentIntent(request, set, policy)
	id, err := recovery.OccurrenceID(intent)
	if err != nil {
		return err
	}
	revision, err := recovery.ScheduleRevisionForInput(intent)
	if err != nil {
		return err
	}
	if receipt.PolicySHA256 != policy || receipt.OccurrenceID != id || occurrence.ID != id || occurrence.PolicySHA256 != policy || occurrence.ScheduleID != intent.ScheduleID || occurrence.ScheduleRevision != revision || occurrence.Scenario != intent.Scenario || occurrence.Operation != intent.Operation || occurrence.PolicyVersion != intent.PolicyVersion || occurrence.TargetScope != intent.TargetScope || occurrence.ArtifactIdentity != intent.ArtifactIdentity || !occurrence.PlannedAt.Equal(request.PlannedAt) || !occurrence.ExpiresAt.Equal(request.ExpiresAt) {
		return errors.New("managed enrollment differs from durable recovery intent")
	}
	return nil
}
