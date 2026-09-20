package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPlatformAdministratorDelegationLifecyclePostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-first@example.test", DisplayName: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-second@example.test", DisplayName: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: first.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-grant-first"})
	if err != nil {
		t.Fatalf("grant first platform administrator: %v", err)
	}
	if grant.Administrator.Principal.ID != first.ID || grant.Administrator.BindingID == "" || grant.Administrator.CreatedAt == "" {
		t.Fatalf("grant result = %#v", grant.Administrator)
	}
	if len(grant.State.Administrators) != 1 {
		t.Fatalf("grant state = %#v", grant.State)
	}
	replay, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: first.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-grant-first"})
	if err != nil {
		t.Fatalf("grant replay: %v", err)
	}
	if !replay.Replayed || replay.Administrator.BindingID != grant.Administrator.BindingID || replay.State.Revision != grant.State.Revision {
		t.Fatalf("grant replay = %#v, want binding %q and revision %q", replay, grant.Administrator.BindingID, grant.State.Revision)
	}
	if _, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: second.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-grant-first"}); !errors.Is(err, access.ErrPlatformAdminIdempotency) {
		t.Fatalf("reused grant key error = %v, want idempotency conflict", err)
	}
	if _, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: second.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-grant-stale"}); !errors.Is(err, access.ErrPlatformAdminStaleRevision) {
		t.Fatalf("stale grant error = %v, want stale revision", err)
	}
	secondGrant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: second.ID, ExpectedRevision: grant.State.Revision, IdempotencyKey: "platform-grant-second"})
	if err != nil {
		t.Fatalf("grant second platform administrator: %v", err)
	}
	revoked, err := repo.RevokePlatformAdmin(ctx, access.PlatformAdminRevokeInput{PrincipalID: first.ID, ExpectedRevision: secondGrant.State.Revision, IdempotencyKey: "platform-revoke-first"})
	if err != nil {
		t.Fatalf("revoke first platform administrator: %v", err)
	}
	if len(revoked.Administrators) != 1 || revoked.Administrators[0].Principal.ID != second.ID {
		t.Fatalf("revoke state = %#v", revoked)
	}
	if _, err := repo.RevokePlatformAdmin(ctx, access.PlatformAdminRevokeInput{PrincipalID: second.ID, ExpectedRevision: revoked.Revision, IdempotencyKey: "platform-revoke-last"}); !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("last administrator revoke error = %v, want protected conflict", err)
	}
	if count := countActivePlatformRoles(t, db, ctx); count != 1 {
		t.Fatalf("active platform role count after last-admin rejection = %d, want 1", count)
	}
}

func TestPlatformAdministratorGrantRecoveryDigestBindingIsIdempotent(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-recovery-binding@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := access.PlatformAdminGrantInput{PrincipalID: principal.ID, ExpectedRevision: state.Revision, IdempotencyKey: "platform-recovery-binding", RequestDigestBinding: "recovery-request-1"}
	first, err := repo.GrantPlatformAdmin(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.GrantPlatformAdmin(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Administrator.BindingID != first.Administrator.BindingID {
		t.Fatalf("recovery replay = %#v, want replayed binding %q", replay, first.Administrator.BindingID)
	}
	input.RequestDigestBinding = "recovery-request-2"
	if _, err := repo.GrantPlatformAdmin(ctx, input); !errors.Is(err, access.ErrPlatformAdminIdempotency) {
		t.Fatalf("changed recovery binding error = %v, want idempotency conflict", err)
	}
}

func TestPlatformAdministratorGrantRequiresEnabledPrincipalPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-disabled@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.admin.QueryRow(ctx, `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp() WHERE id=$1::uuid RETURNING id`, principal.ID).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	state, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: principal.ID, ExpectedRevision: state.Revision, IdempotencyKey: "platform-grant-disabled"}); !errors.Is(err, access.ErrPlatformAdminConflict) {
		t.Fatalf("disabled principal grant error = %v, want conflict", err)
	}
}

func TestPlatformAdministratorLifecycleProtectsSoleUsableAdministratorPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	admin, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-sole-lifecycle@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: admin.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-sole-lifecycle-grant"}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.DisablePrincipal(ctx, admin.ID); !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("sole administrator block error = %v, want last-admin conflict", err)
	}
	if _, err := repo.DisableProvisionedPrincipal(ctx, admin.ID); !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("sole administrator disable error = %v, want last-admin conflict", err)
	}
	if err := repo.DeletePrincipal(ctx, admin.ID); !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("sole administrator delete error = %v, want last-admin conflict", err)
	}
	if count := countActivePlatformRoles(t, db, ctx); count != 1 {
		t.Fatalf("active role count after lifecycle rejection = %d, want 1", count)
	}
	current, err := repo.PrincipalByID(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AccessDisabled() {
		t.Fatalf("sole administrator became disabled after rejected lifecycle: %#v", current)
	}
}

func TestPlatformAdministratorLifecycleRevokesRoleAndRequiresExplicitRegrantPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-lifecycle-first@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-lifecycle-second@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstGrant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: first.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-lifecycle-first-grant"})
	if err != nil {
		t.Fatal(err)
	}
	secondGrant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: second.ID, ExpectedRevision: firstGrant.State.Revision, IdempotencyKey: "platform-lifecycle-second-grant"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.DisablePrincipal(ctx, first.ID); err != nil {
		t.Fatalf("disable first administrator: %v", err)
	}
	state, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Administrators) != 1 || state.Administrators[0].Principal.ID != second.ID {
		t.Fatalf("state after first administrator disable = %#v", state)
	}
	if _, err := repo.EnablePrincipal(ctx, first.ID); err != nil {
		t.Fatalf("re-enable first principal: %v", err)
	}
	state, err = repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Administrators) != 1 || state.Administrators[0].Principal.ID != second.ID {
		t.Fatalf("re-enable silently revived platform role: %#v", state)
	}
	var firstActiveBinding int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.platform_role_binding WHERE principal_id=$1::uuid AND role='platform_admin' AND revoked_at IS NULL`, first.ID).Scan(&firstActiveBinding); err != nil {
		t.Fatal(err)
	}
	if firstActiveBinding != 0 {
		t.Fatalf("re-enabled principal retained active platform binding: %d", firstActiveBinding)
	}

	regrant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: first.ID, ExpectedRevision: state.Revision, IdempotencyKey: "platform-lifecycle-first-regrant"})
	if err != nil {
		t.Fatalf("explicit platform role regrant: %v", err)
	}
	if regrant.Administrator.BindingID == firstGrant.Administrator.BindingID {
		t.Fatal("explicit regrant reused the revoked platform binding")
	}
	if _, err := repo.DisablePrincipal(ctx, second.ID); err != nil {
		t.Fatalf("disable remaining administrator after explicit regrant: %v", err)
	}
	finalState, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalState.Administrators) != 1 || finalState.Administrators[0].Principal.ID != first.ID {
		t.Fatalf("state after second administrator disable = %#v", finalState)
	}
	_ = secondGrant
}

func TestPlatformAdministratorConcurrentDeletionPreservesLastUsableAdministratorPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-concurrent-delete-first@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-concurrent-delete-second@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstGrant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: first.ID, ExpectedRevision: empty.Revision, IdempotencyKey: "platform-concurrent-delete-first-grant"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: second.ID, ExpectedRevision: firstGrant.State.Revision, IdempotencyKey: "platform-concurrent-delete-second-grant"}); err != nil {
		t.Fatal(err)
	}

	type deletionResult struct {
		principalID string
		err         error
	}
	results := make(chan deletionResult, 2)
	var wg sync.WaitGroup
	for _, principalID := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			results <- deletionResult{principalID: id, err: repo.DeletePrincipal(ctx, id)}
		}(principalID)
	}
	wg.Wait()
	close(results)

	var deletedID string
	var rejected int
	for result := range results {
		if result.err == nil {
			deletedID = result.principalID
			continue
		}
		if errors.Is(result.err, access.ErrPlatformAdminLastAdmin) {
			rejected++
			continue
		}
		t.Fatalf("concurrent deletion of %s: %v", result.principalID, result.err)
	}
	if deletedID == "" || rejected != 1 {
		t.Fatalf("concurrent deletion results deleted=%q rejected=%d, want one of each", deletedID, rejected)
	}
	state, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Administrators) != 1 || state.Administrators[0].Principal.ID == deletedID {
		t.Fatalf("usable administrators after concurrent deletion = %#v", state)
	}
	if count := countActivePlatformRoles(t, db, ctx); count != 1 {
		t.Fatalf("active platform role count after concurrent deletion = %d, want 1", count)
	}
}

func TestPlatformAdministratorDelegationAndAuditCommitTogetherPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-audited@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var grant access.PlatformAdminGrantResult
	if err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		writer, ok := txRepo.(access.PlatformAdminWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator writer unavailable")
		}
		grant, err = writer.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: principal.ID, ExpectedRevision: state.Revision, IdempotencyKey: "platform-audited-grant"})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{PrincipalID: principal.ID, Action: "platform_admin.granted", ResourceKind: "platform_role_binding", ResourceID: grant.Administrator.BindingID, Status: "success", MetadataJSON: `{}`}, nil
	}); err != nil {
		t.Fatalf("audited platform administrator grant: %v", err)
	}
	var auditCount int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE action='platform_admin.granted' AND resource_id=$1`, grant.Administrator.BindingID).Scan(&auditCount); err != nil {
		t.Fatalf("count platform administrator audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("platform administrator audit count = %d, want 1", auditCount)
	}

	rollbackPrincipal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "platform-audited-rollback@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	committedState, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		writer, ok := txRepo.(access.PlatformAdminWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator writer unavailable")
		}
		if _, err := writer.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: rollbackPrincipal.ID, ExpectedRevision: committedState.Revision, IdempotencyKey: "platform-audited-rollback"}); err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{}, errors.New("force audited mutation rollback")
	}); err == nil {
		t.Fatal("audited platform administrator rollback unexpectedly committed")
	}
	finalState, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if finalState.Revision != committedState.Revision || len(finalState.Administrators) != len(committedState.Administrators) {
		t.Fatalf("rolled-back delegation changed state: before=%#v after=%#v", committedState, finalState)
	}
}

func TestPlatformRoleApprovalLifecycleAndConcurrentApprovalPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	requester, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "approval-requester@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	approver, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "approval-approver@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "approval-target@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: requester.ID, ExpectedRevision: state.Revision, IdempotencyKey: "approval-bootstrap-requester"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: approver.ID, ExpectedRevision: state.Revision, IdempotencyKey: "approval-bootstrap-approver"}); err != nil {
		t.Fatal(err)
	}
	state, err = repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}

	request, err := repo.RequestPlatformRoleApproval(ctx, access.PlatformRoleApprovalRequestInput{
		Action: "grant", PrincipalID: target.ID, RequesterID: requester.ID,
		ExpectedRevision: state.Revision, IdempotencyKey: "approval-request-grant",
	})
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	replay, err := repo.RequestPlatformRoleApproval(ctx, access.PlatformRoleApprovalRequestInput{
		Action: "grant", PrincipalID: target.ID, RequesterID: requester.ID,
		ExpectedRevision: state.Revision, IdempotencyKey: "approval-request-grant",
	})
	if err != nil || replay.ID != request.ID || replay.Revision != request.Revision {
		t.Fatalf("request replay = %#v, err=%v; want original %#v", replay, err, request)
	}
	if _, err := repo.ApprovePlatformRoleApproval(ctx, access.PlatformRoleApprovalDecisionInput{ApprovalID: request.ID, ActorID: requester.ID, ExpectedRevision: request.Revision, IdempotencyKey: "approval-self-approve"}); !errors.Is(err, access.ErrPlatformRoleApprovalSeparationOfDuty) {
		t.Fatalf("self approval error = %v, want separation-of-duty", err)
	}

	// Two concurrent approvals race on the authority lock; exactly one may
	// transition the pending row at its expected revision.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"approval-approve-a", "approval-approve-b"} {
		wg.Add(1)
		go func(idempotencyKey string) {
			defer wg.Done()
			_, approveErr := repo.ApprovePlatformRoleApproval(ctx, access.PlatformRoleApprovalDecisionInput{
				ApprovalID: request.ID, ActorID: approver.ID, ExpectedRevision: request.Revision, IdempotencyKey: idempotencyKey,
			})
			results <- approveErr
		}(key)
	}
	wg.Wait()
	close(results)
	var approved int
	for approveErr := range results {
		if approveErr == nil {
			approved++
			continue
		}
		if !errors.Is(approveErr, access.ErrPlatformRoleApprovalConflict) && !errors.Is(approveErr, access.ErrPlatformRoleApprovalExpired) {
			t.Fatalf("concurrent approval error = %v", approveErr)
		}
	}
	if approved != 1 {
		t.Fatalf("concurrent approval successes = %d, want 1", approved)
	}
	approvedRequest, err := repo.GetPlatformRoleApproval(ctx, request.ID)
	if err != nil || approvedRequest.Status != access.PlatformRoleApprovalApproved {
		t.Fatalf("approved request = %#v, err=%v", approvedRequest, err)
	}

	executed, err := repo.ExecutePlatformRoleApproval(ctx, access.PlatformRoleApprovalExecuteInput{ApprovalID: request.ID, ActorID: approver.ID, IdempotencyKey: "approval-execute"})
	if err != nil {
		t.Fatalf("execute approval: %v", err)
	}
	if executed.Status != access.PlatformRoleApprovalExecuted || executed.BindingID == "" || executed.ResultRevision == "" {
		t.Fatalf("executed approval = %#v", executed)
	}
	executedReplay, err := repo.ExecutePlatformRoleApproval(ctx, access.PlatformRoleApprovalExecuteInput{ApprovalID: request.ID, ActorID: approver.ID, IdempotencyKey: "approval-execute"})
	if err != nil || executedReplay.ID != executed.ID || executedReplay.Revision != executed.Revision {
		t.Fatalf("execute replay = %#v, err=%v; want %#v", executedReplay, err, executed)
	}
	finalState, err := repo.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalState.Administrators) != len(state.Administrators)+1 {
		t.Fatalf("final administrators = %#v, want target grant", finalState)
	}

	// The approval and the role mutation are durable in the same database;
	// this also guards against the request operation being replayed from a
	// different connection after the original transaction committed.
	var operationCount int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.platform_role_approval_operation WHERE approval_id=$1::uuid`, request.ID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 3 {
		t.Fatalf("approval operation count = %d, want request + approve + execute", operationCount)
	}

	cancelRequest, err := repo.RequestPlatformRoleApproval(ctx, access.PlatformRoleApprovalRequestInput{
		Action: "revoke", PrincipalID: target.ID, RequesterID: requester.ID,
		ExpectedRevision: finalState.Revision, IdempotencyKey: "approval-request-cancel",
	})
	if err != nil {
		t.Fatalf("request cancelable approval: %v", err)
	}
	canceled, err := repo.CancelPlatformRoleApproval(ctx, access.PlatformRoleApprovalDecisionInput{
		ApprovalID: cancelRequest.ID, ActorID: requester.ID, ExpectedRevision: cancelRequest.Revision, IdempotencyKey: "approval-cancel",
	})
	if err != nil || canceled.Status != access.PlatformRoleApprovalCanceled {
		t.Fatalf("canceled approval = %#v, err=%v", canceled, err)
	}

	expireRequest, err := repo.RequestPlatformRoleApproval(ctx, access.PlatformRoleApprovalRequestInput{
		Action: "grant", PrincipalID: target.ID, RequesterID: requester.ID,
		ExpectedRevision: finalState.Revision, IdempotencyKey: "approval-request-expire",
	})
	if err != nil {
		t.Fatalf("request expirable approval: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `UPDATE access.platform_role_approval SET expires_at=clock_timestamp() - interval '1 second' WHERE id=$1::uuid`, expireRequest.ID); err != nil {
		t.Fatalf("force approval expiry in test: %v", err)
	}
	expired, err := repo.ExpirePlatformRoleApproval(ctx, access.PlatformRoleApprovalDecisionInput{
		ApprovalID: expireRequest.ID, ActorID: approver.ID, ExpectedRevision: expireRequest.Revision, IdempotencyKey: "approval-expire",
	})
	if err != nil || expired.Status != access.PlatformRoleApprovalExpired {
		t.Fatalf("expired approval = %#v, err=%v", expired, err)
	}
	_ = grant
}

func countActivePlatformRoles(t *testing.T, db auditDatabase, ctx context.Context) int {
	t.Helper()
	var count int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.platform_role_binding WHERE role='platform_admin' AND revoked_at IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
