package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// PrivacyExecution reports committed progress only. One call executes at most
// 50 items; a caller can resume the same reviewed case until Status completes.
type PrivacyExecution struct {
	RunID        string
	Status       string // pending, completed, or incomplete when this call failed
	Cursor       int64
	StoreResults []PrivacyStoreResult
}

type PrivacyStoreResult struct {
	Store   string
	Status  string // completed or excluded
	Outcome string
	Count   int64
}

func (e *PrivacyExecutor) beginPrivacyTx(ctx context.Context, readOnly bool) (pgx.Tx, error) {
	beginner, ok := e.repo.db.(interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	})
	if !ok {
		return nil, errors.New("privacy executor requires a PostgreSQL transaction-capable database")
	}
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead}
	if readOnly {
		opts.AccessMode = pgx.ReadOnly
	}
	return beginner.BeginTx(ctx, opts)
}

func privacyRunKey(g PrivacySubjectGraph) accessdb.LockPrivacyActionRunParams {
	return accessdb.LockPrivacyActionRunParams{CustomerID: g.Boundary.CustomerID,
		DeploymentID: g.Boundary.DeploymentID, CaseID: g.CaseID, Action: g.Action}
}

func privacyRunMatches(run accessdb.AccessPrivacyActionRun, g PrivacySubjectGraph, approvedDigest string) bool {
	actorID, _ := pgUUID(g.ActorID)
	principalID, _ := pgUUID(g.PrincipalID)
	return run.GraphDigest == privacyGraphDigest(g) && run.ManifestDigest == approvedDigest &&
		run.CorrelationID == g.CorrelationID && run.ActorID == actorID && run.PrincipalID == principalID &&
		run.PrincipalType == g.PrincipalType
}

// ensurePrivacyRun binds the exact reviewed graph and complete dry-run digest
// in one repeatable-read transaction before any effect is attempted.
func (e *PrivacyExecutor) ensurePrivacyRun(ctx context.Context, g PrivacySubjectGraph, approvedDigest string) (pgtype.UUID, error) {
	tx, err := e.beginPrivacyTx(ctx, false)
	if err != nil {
		return pgtype.UUID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := accessdb.New(tx)
	principalID, err := e.authorize(ctx, tx, g)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if _, err = q.LockPrivacyPrincipal(ctx, principalID); err != nil {
		return pgtype.UUID{}, privacyShortError(err)
	}
	run, err := q.LockPrivacyActionRun(ctx, privacyRunKey(g))
	if err == nil {
		if !privacyRunMatches(run, g, approvedDigest) {
			return pgtype.UUID{}, ErrPrivacyConflict
		}
		return run.RunID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, privacyShortError(err)
	}
	// A stable snapshot is required: the approved manifest must still be the
	// complete store graph when the execution plan is persisted.
	digest, _, err := scanPrivacyManifest(ctx, tx, g, principalID, nil)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if digest != approvedDigest {
		return pgtype.UUID{}, ErrPrivacyConflict
	}
	runString, err := newUUID()
	if err != nil {
		return pgtype.UUID{}, err
	}
	runID, err := pgUUID(runString)
	if err != nil {
		return pgtype.UUID{}, err
	}
	actorID, _ := pgUUID(g.ActorID)
	tag, err := q.InsertPrivacyActionRun(ctx, accessdb.InsertPrivacyActionRunParams{
		RunID: runID, CustomerID: g.Boundary.CustomerID, DeploymentID: g.Boundary.DeploymentID,
		CaseID: g.CaseID, CorrelationID: g.CorrelationID, ActorID: actorID, PrincipalID: principalID,
		PrincipalType: g.PrincipalType, Action: g.Action, GraphDigest: privacyGraphDigest(g), ManifestDigest: digest,
	})
	if err != nil {
		return pgtype.UUID{}, privacyShortError(err)
	}
	if tag.RowsAffected() == 0 {
		return pgtype.UUID{}, ErrPrivacyBusy // a competing planner won; retry and compare its graph
	}
	_, _, err = scanPrivacyManifest(ctx, tx, g, principalID, func(ordinal int64, record PrivacyRecord) error {
		status := "pending"
		if record.Actionability == "excluded" {
			status = "excluded"
		}
		return q.InsertPrivacyActionItem(ctx, accessdb.InsertPrivacyActionItemParams{
			RunID: runID, Ordinal: ordinal, Store: record.Store, RecordID: record.RecordID,
			Actionable: status == "pending", Status: status,
		})
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return pgtype.UUID{}, privacyShortError(err)
	}
	return runID, nil
}

func privacyAudit(ctx context.Context, repo *Repository, g PrivacySubjectGraph, store, status string, count int64) error {
	metadata, _ := json.Marshal(struct {
		Count int64 `json:"count"`
	}{count})
	return repo.RecordAuditEvent(ctx, access.AuditEventInput{
		PrincipalID: g.ActorID, Action: "privacy.restrict_access", ResourceKind: store,
		Status: status, RequestID: g.CaseID, CorrelationID: g.CorrelationID, MetadataJSON: string(metadata),
	})
}

func (e *PrivacyExecutor) applyPrivacyItem(ctx context.Context, tx pgx.Tx, g PrivacySubjectGraph, item accessdb.ListPendingPrivacyActionItemsRow, principalID pgtype.UUID) (string, error) {
	q := accessdb.New(tx)
	repo := &Repository{db: tx, fingerprintKey: e.repo.fingerprintKey}
	var affected int64
	switch item.Store {
	case "access.principal":
		_, err := repo.DisablePrincipal(ctx, g.PrincipalID)
		if err != nil {
			return "", err
		}
		return "restricted", nil
	case "access.session":
		if err := repo.RevokeSessionForPrincipal(ctx, g.PrincipalID, item.RecordID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "already_restricted", nil
			}
			return "", err
		}
		return "restricted", nil
	case "access.api_token":
		if err := repo.RevokeAPITokenForPrincipal(ctx, g.PrincipalID, item.RecordID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "already_restricted", nil
			}
			return "", err
		}
		return "restricted", nil
	case "access.service_principal_secret":
		if err := repo.RevokeServicePrincipalSecret(ctx, g.PrincipalID, item.RecordID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "already_restricted", nil
			}
			return "", err
		}
		return "restricted", nil
	case "access.local_credential":
		if item.RecordID != g.PrincipalID {
			return "", ErrPrivacyConflict
		}
		if err := q.RevokePrincipalLocalCredential(ctx, principalID); err != nil {
			return "", err
		}
		return "restricted", nil
	case "access.principal_group", "access.platform_role_binding":
		id, err := pgUUID(item.RecordID)
		if err != nil {
			return "", err
		}
		if item.Store == "access.principal_group" {
			tag, err := q.RevokePrivacyGroupMembership(ctx, accessdb.RevokePrivacyGroupMembershipParams{MembershipID: id, PrincipalID: principalID})
			if err != nil {
				return "", err
			}
			affected = tag.RowsAffected()
		} else {
			tag, err := q.RevokePrivacyPlatformRole(ctx, accessdb.RevokePrivacyPlatformRoleParams{RoleID: id, PrincipalID: principalID})
			if err != nil {
				return "", err
			}
			affected = tag.RowsAffected()
		}
	case "access.authoring_session":
		var err error
		affected, err = q.RevokePrivacyAuthoringSession(ctx, accessdb.RevokePrivacyAuthoringSessionParams{SessionID: item.RecordID, PrincipalID: principalID})
		if err != nil {
			return "", err
		}
	case "access.oauth_session":
		kind, digest, found := strings.Cut(item.RecordID, ":")
		if !found || len(digest) != 64 {
			return "", ErrPrivacyConflict
		}
		tag, err := q.RevokePrivacyOAuthSession(ctx, accessdb.RevokePrivacyOAuthSessionParams{
			Kind: kind, SignatureDigest: digest, PrincipalID: g.PrincipalID,
		})
		if err != nil {
			return "", err
		}
		affected = tag.RowsAffected()
	default:
		return "", ErrPrivacyUnsupported
	}
	if affected == 0 {
		return "already_restricted", nil
	}
	return "restricted", nil
}

// Execute processes one bounded batch. An error never commits a partial batch;
// a later call with the same reviewed graph/digest resumes committed items.
func (e *PrivacyExecutor) Execute(ctx context.Context, graph PrivacySubjectGraph, approvedManifestDigest string, batchSize int) (PrivacyExecution, error) {
	result := PrivacyExecution{Status: "incomplete"}
	if graph.DryRun {
		return result, errors.New("execution cannot be a dry-run")
	}
	g, err := e.validateGraph(graph)
	if err != nil {
		return result, err
	}
	if batchSize <= 0 || batchSize > 50 {
		return result, errors.New("privacy batch size is out of bounds")
	}
	if len(approvedManifestDigest) != len("sha256:")+64 || !strings.HasPrefix(approvedManifestDigest, "sha256:") {
		return result, ErrPrivacyConflict
	}
	runID, err := e.ensurePrivacyRun(ctx, g, approvedManifestDigest)
	if err != nil {
		return result, err
	}
	result.RunID = principalUUID(runID)
	tx, err := e.beginPrivacyTx(ctx, false)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := accessdb.New(tx)
	principalID, err := e.authorize(ctx, tx, g)
	if err != nil {
		return result, err
	}
	if _, err = q.LockPrivacyPrincipal(ctx, principalID); err != nil {
		return result, privacyShortError(err)
	}
	run, err := q.LockPrivacyActionRun(ctx, privacyRunKey(g))
	if err != nil {
		return result, privacyShortError(err)
	}
	if !privacyRunMatches(run, g, approvedManifestDigest) || run.RunID != runID {
		return result, ErrPrivacyConflict
	}
	result.Cursor = run.Cursor
	if run.Status == "completed" {
		result.Status = "completed"
		return e.privacyCommittedResult(ctx, tx, runID, result, false)
	}
	items, err := q.ListPendingPrivacyActionItems(ctx, accessdb.ListPendingPrivacyActionItemsParams{RunID: runID, PageSize: int32(batchSize)})
	if err != nil {
		return result, privacyShortError(err)
	}
	counts := map[string]int64{}
	for _, item := range items {
		outcome, actionErr := e.applyPrivacyItem(ctx, tx, g, item, principalID)
		if actionErr != nil {
			_ = tx.Rollback(ctx)
			if auditErr := privacyAudit(ctx, e.repo, g, item.Store, "failed", 0); auditErr != nil {
				return result, errors.Join(fmt.Errorf("privacy store %s: %w", item.Store, actionErr), fmt.Errorf("record privacy failure audit: %w", auditErr))
			}
			return result, fmt.Errorf("privacy store %s: %w", item.Store, actionErr)
		}
		tag, actionErr := q.CompletePrivacyActionItem(ctx, accessdb.CompletePrivacyActionItemParams{RunID: runID, Ordinal: item.Ordinal, Outcome: outcome})
		if actionErr != nil || tag.RowsAffected() != 1 {
			return result, ErrPrivacyConflict
		}
		counts[item.Store]++
	}
	result.Cursor += int64(len(items))
	if len(items) > 0 {
		if err := q.AdvancePrivacyActionRun(ctx, accessdb.AdvancePrivacyActionRunParams{RunID: runID, Cursor: result.Cursor}); err != nil {
			return result, err
		}
	}
	for store, count := range counts {
		if err := privacyAudit(ctx, &Repository{db: tx, fingerprintKey: e.repo.fingerprintKey}, g, store, "success", count); err != nil {
			return result, err
		}
	}
	remaining, err := q.ListPendingPrivacyActionItems(ctx, accessdb.ListPendingPrivacyActionItemsParams{RunID: runID, PageSize: 1})
	if err != nil {
		return result, err
	}
	if len(remaining) == 0 {
		tag, err := q.CompletePrivacyActionRun(ctx, runID)
		if err != nil || tag.RowsAffected() != 1 {
			return result, ErrPrivacyConflict
		}
		if err := privacyAudit(ctx, &Repository{db: tx, fingerprintKey: e.repo.fingerprintKey}, g, "privacy_action_run", "success", result.Cursor); err != nil {
			return result, err
		}
		result.Status = "completed"
	} else {
		result.Status = "pending"
	}
	return e.privacyCommittedResult(ctx, tx, runID, result, true)
}

func (e *PrivacyExecutor) privacyCommittedResult(ctx context.Context, tx pgx.Tx, runID pgtype.UUID, result PrivacyExecution, commit bool) (PrivacyExecution, error) {
	rows, err := accessdb.New(tx).ListPrivacyActionStoreResults(ctx, runID)
	if err != nil {
		return PrivacyExecution{RunID: result.RunID, Status: "incomplete"}, err
	}
	for _, row := range rows {
		result.StoreResults = append(result.StoreResults, PrivacyStoreResult{Store: row.Store, Status: row.Status, Outcome: row.Outcome, Count: row.ItemCount})
	}
	if commit {
		if err := tx.Commit(ctx); err != nil {
			return PrivacyExecution{RunID: result.RunID, Status: "incomplete"}, err
		}
	}
	return result, nil
}
