package postgres

import (
	"context"
	"errors"
	"fmt"

	refreshdb "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/jackc/pgx/v5"
)

// ClaimExact claims only an explicitly enrolled recovery occurrence. It never
// selects, expires or reclaims unrelated queue work. Expired execution leases
// use the existing attempt abandonment and generation fencing rules.
func (r *RecoveryLedger) ClaimExact(ctx context.Context, id string, in recovery.ClaimInput) (recovery.Occurrence, bool, error) {
	if id == "" {
		return recovery.Occurrence{}, false, fmt.Errorf("exact recovery occurrence required")
	}
	if err := in.Validate(); err != nil {
		return recovery.Occurrence{}, false, err
	}
	var result recovery.Occurrence
	var claimed bool
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		if _, err := queries.LockExactRecoveryQualificationOccurrence(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		occurrence, _, err := readOccurrence(ctx, tx, id)
		if err != nil {
			return err
		}
		if (occurrence.Status == recovery.StatusClaimed || occurrence.Status == recovery.StatusRunning) && !occurrence.LeaseExpiresAt.After(in.Now) {
			if err := queries.AbandonRecoveryQualificationAttempt(ctx, refreshdb.AbandonRecoveryQualificationAttemptParams{FinishedAt: timestamp(in.Now), OccurrenceID: id, FenceGeneration: occurrence.Fence.Generation}); err != nil {
				return err
			}
			if err := queries.RequeueRecoveryQualificationOccurrence(ctx, refreshdb.RequeueRecoveryQualificationOccurrenceParams{OccurrenceID: id, FenceGeneration: occurrence.Fence.Generation}); err != nil {
				return err
			}
			occurrence.Status = recovery.StatusPending
		}
		if occurrence.Status != recovery.StatusPending || occurrence.PlannedAt.After(in.Now) || !occurrence.ExpiresAt.After(in.Now) {
			return nil
		}
		expires := in.Now.Add(in.Lease).UTC()
		claim, err := queries.ClaimRecoveryQualificationOccurrence(ctx, refreshdb.ClaimRecoveryQualificationOccurrenceParams{ClaimedAt: timestamp(in.Now), LeaseOwner: in.WorkerID, LeaseExpiresAt: timestamp(expires), Actor: in.Actor, OccurrenceID: id})
		if err != nil {
			return err
		}
		if err := queries.InsertRecoveryQualificationAttempt(ctx, refreshdb.InsertRecoveryQualificationAttemptParams{OccurrenceID: id, AttemptNumber: claim.AttemptCount, FenceGeneration: claim.FenceGeneration, WorkerID: in.WorkerID, Actor: in.Actor, ClaimedAt: in.Now.UTC(), LeaseExpiresAt: expires}); err != nil {
			return err
		}
		result, _, err = readOccurrence(ctx, tx, id)
		claimed = err == nil
		return err
	})
	return result, claimed, err
}
