package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
)

// ExternalAttemptReconciliation is restart evidence supplied by a local
// DuckDB session. A missing marker is not sufficient to retry while the
// session may still commit; callers must provide positive termination evidence.
type ExternalAttemptReconciliation struct {
	AttemptID           string
	OwnerID             string
	FencingEpoch        int64
	Marker              ducklake.CommitMarker
	Snapshot            SnapshotRef
	Local               ducklake.SnapshotLookup
	SessionTerminated   bool
	TerminationEvidence json.RawMessage
	ReconciledAt        time.Time
}

// ReconcileExternalAttempt resolves the exact persistent DuckLake marker and
// records one terminal control outcome. It never retries an attempt based only
// on a lease timeout or connection-local last_committed_snapshot value.
func ReconcileExternalAttempt(ctx context.Context, tx DBTX, in ExternalAttemptReconciliation) (AttemptEvidence, error) {
	if tx == nil || in.Local == nil || !validUUID(in.AttemptID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || !validSnapshotRef(in.Snapshot) {
		return AttemptEvidence{}, ErrInvalid
	}
	if in.Marker.AttemptID != in.AttemptID || in.Marker.RequestDigest == "" || in.Marker.PlanDigest == "" || in.Marker.PhysicalPoolID != in.Snapshot.PhysicalPoolID || in.Marker.LeaseEpoch != in.FencingEpoch {
		return AttemptEvidence{}, fmt.Errorf("%w: external marker identity mismatch", ErrConflict)
	}
	markerJSON, err := in.Marker.CanonicalJSON()
	if err != nil {
		return AttemptEvidence{}, err
	}
	snapshotID, lookupErr := ducklake.ResolveCommittedSnapshot(ctx, in.Local, in.Marker)
	if lookupErr == nil {
		if snapshotID != in.Snapshot.SnapshotID {
			return AttemptEvidence{}, fmt.Errorf("%w: resolved snapshot %d differs from expected %d", ErrConflict, snapshotID, in.Snapshot.SnapshotID)
		}
		return CommitAttempt(ctx, tx, CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, Snapshot: in.Snapshot, CommitMarker: markerJSON, CommittedAt: in.ReconciledAt})
	}

	// A lookup miss only proves that this catalog connection cannot find the
	// marker. It is not positive termination evidence: the session may still
	// commit, or the lookup may be incomplete. Require an explicitly supplied,
	// strict bounded evidence object before allowing an abort transition.
	positiveTermination := false
	if _, evidenceErr := canonicalEvidence(in.TerminationEvidence); evidenceErr == nil {
		positiveTermination = true
	}
	evidence := in.TerminationEvidence
	if !positiveTermination {
		encoded, _ := json.Marshal(map[string]string{"resolver_error": lookupErr.Error()})
		evidence = encoded
	}
	if in.SessionTerminated && positiveTermination && errors.Is(lookupErr, ducklake.ErrCommittedSnapshotNotFound) {
		return TerminateAttempt(ctx, tx, TerminateAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, Evidence: evidence, TerminatedAt: in.ReconciledAt}, AttemptAborted)
	}
	// Duplicate markers, malformed markers, and an unknown session outcome are
	// all indeterminate. The caller must create a successor UUID/namespace.
	return TerminateAttempt(ctx, tx, TerminateAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, Evidence: evidence, TerminatedAt: in.ReconciledAt}, AttemptIndeterminate)
}

func (r *Repository) ReconcileExternalAttempt(ctx context.Context, in ExternalAttemptReconciliation) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return ReconcileExternalAttempt(ctx, r.db, in)
}
