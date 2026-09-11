package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/jackc/pgx/v5"
)

const maxAuthorizationPolicyIdempotencyKeyBytes = 256

type authorizationPolicyHead struct {
	Scope    access.AuthorizationPolicyScope
	Revision int64
	Digest   string
}

func (r *Repository) validateAuthorizationPolicyScope(scope access.AuthorizationPolicyScope) error {
	if r != nil && r.authorizationScope != nil && *r.authorizationScope != scope {
		return fmt.Errorf("%w: configured target/project/environment is %s/%s/%s, requested %s/%s/%s", access.ErrAuthorizationPolicyConflict,
			r.authorizationScope.TargetID, r.authorizationScope.ProjectID, r.authorizationScope.Environment,
			scope.TargetID, scope.ProjectID, scope.Environment)
	}
	return access.ValidateAuthorizationPolicyScope(scope)
}

func policyScopeParams(scope access.AuthorizationPolicyScope) accessdb.GetAuthorizationPolicyHeadParams {
	return accessdb.GetAuthorizationPolicyHeadParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment}
}

func policyScopeLockParams(scope access.AuthorizationPolicyScope) accessdb.LockAuthorizationPolicyHeadParams {
	return accessdb.LockAuthorizationPolicyHeadParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment}
}

func policyScopeParamsForShare(scope access.AuthorizationPolicyScope) accessdb.LockAuthorizationPolicyHeadForShareParams {
	return accessdb.LockAuthorizationPolicyHeadForShareParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment}
}

func policyScopeRevisionParams(scope access.AuthorizationPolicyScope, revision int64) accessdb.GetAuthorizationPolicyRevisionParams {
	return accessdb.GetAuthorizationPolicyRevisionParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: revision}
}

func policyHeadFromRow(row accessdb.GetAuthorizationPolicyHeadRow) authorizationPolicyHead {
	return authorizationPolicyHead{Scope: access.AuthorizationPolicyScope{TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment}, Revision: row.Revision, Digest: row.Digest}
}

func policyHeadFromLockRow(row accessdb.LockAuthorizationPolicyHeadRow) authorizationPolicyHead {
	return authorizationPolicyHead{Scope: access.AuthorizationPolicyScope{TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment}, Revision: row.Revision, Digest: row.Digest}
}

func authorizationPolicyRoleBindingFromRow(id, name, kind, subjectID, role string, encoded []byte) (access.RoleBinding, error) {
	var capabilities []access.Capability
	if err := json.Unmarshal(encoded, &capabilities); err != nil {
		return access.RoleBinding{}, fmt.Errorf("decode authorization role binding %q capabilities: %w", id, err)
	}
	binding := access.RoleBinding{ID: id, Name: name, Subject: access.SubjectRef{Kind: access.SubjectKind(kind), ID: subjectID}, Role: access.ProjectRole(role), Capabilities: capabilities}
	if err := accesssnapshot.ValidateRoleBinding(binding); err != nil {
		return access.RoleBinding{}, fmt.Errorf("authorization role binding %q: %w", id, err)
	}
	return binding, nil
}

func (r *Repository) authorizationPolicyAtRevision(ctx context.Context, db DBTX, scope access.AuthorizationPolicyScope, revision int64, expectedDigest string) (access.AuthorizationPolicy, error) {
	if revision <= 0 {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy revision must be positive", access.ErrAuthorizationPolicyConflict)
	}
	queries := accessdb.New(db)
	row, err := queries.GetAuthorizationPolicyRevision(ctx, policyScopeRevisionParams(scope, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: revision %d", access.ErrAuthorizationPolicyNotFound, revision)
	}
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("read authorization policy revision: %w", err)
	}
	if row.TargetID != scope.TargetID || row.ProjectID != scope.ProjectID || row.Environment != scope.Environment || row.Revision != revision {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy revision scope mismatch", access.ErrAuthorizationPolicyConflict)
	}
	rows, err := queries.ListAuthorizationPolicyRoleBindings(ctx, accessdb.ListAuthorizationPolicyRoleBindingsParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: revision})
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("read authorization policy role bindings: %w", err)
	}
	bindings := make([]access.RoleBinding, 0, len(rows))
	for _, item := range rows {
		binding, bindingErr := authorizationPolicyRoleBindingFromRow(item.ID, item.Name, item.SubjectKind, item.SubjectID, item.Role, item.Capabilities)
		if bindingErr != nil {
			return access.AuthorizationPolicy{}, bindingErr
		}
		bindings = append(bindings, binding)
	}
	// SQL orders by ID, but sort again before validation/digesting so this
	// boundary remains deterministic if the query is ever changed.
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].ID < bindings[j].ID })
	digest, err := access.AuthorizationPolicyDigest(scope, bindings)
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("compute authorization policy digest: %w", err)
	}
	if digest != row.Digest || (expectedDigest != "" && expectedDigest != row.Digest) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: stored digest %q, computed %q", access.ErrAuthorizationPolicyConflict, row.Digest, digest)
	}
	return access.AuthorizationPolicy{Scope: scope, Revision: revision, Digest: row.Digest, RoleBindings: cloneAuthorizationRoleBindings(bindings)}, nil
}

// AuthorizationPolicy reads the current exact target/project/environment
// policy. Missing state is an error; callers must not interpret absence as an
// empty authorization document.
func (r *Repository) AuthorizationPolicy(ctx context.Context, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	if err := r.validateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	row, err := accessdb.New(db).GetAuthorizationPolicyHead(ctx, policyScopeParams(scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: target=%s project=%s environment=%s", access.ErrAuthorizationPolicyNotFound, scope.TargetID, scope.ProjectID, scope.Environment)
	}
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("read authorization policy head: %w", err)
	}
	return r.authorizationPolicyAtRevision(ctx, db, scope, row.Revision, row.Digest)
}

// AuthorizationPolicyRevision reads an immutable historical policy revision.
func (r *Repository) AuthorizationPolicyRevision(ctx context.Context, scope access.AuthorizationPolicyScope, revision int64) (access.AuthorizationPolicy, error) {
	if err := r.validateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	return r.authorizationPolicyAtRevision(ctx, db, scope, revision, "")
}

// AuthorizationPolicyDigest reads the immutable revision identified by its
// canonical digest. A digest is qualified by target/project/environment and
// never resolves across policy scopes.
func (r *Repository) AuthorizationPolicyDigest(ctx context.Context, scope access.AuthorizationPolicyScope, digest string) (access.AuthorizationPolicy, error) {
	if err := r.validateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if !canonicalPolicyDigest(digest) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy digest is invalid", access.ErrAuthorizationPolicyConflict)
	}
	db, err := r.requireDB()
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	row, err := accessdb.New(db).GetAuthorizationPolicyRevisionByDigest(ctx, accessdb.GetAuthorizationPolicyRevisionByDigestParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Digest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy digest %s", access.ErrAuthorizationPolicyNotFound, digest)
	}
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("read authorization policy digest: %w", err)
	}
	return r.authorizationPolicyAtRevision(ctx, db, scope, row.Revision, digest)
}

// ValidateAuthorizationPolicyRevisionTx locks the target policy head in the
// caller-owned transaction and validates the exact revision/digest that the
// transaction is about to seal. It returns the detached historical document
// so generation admission cannot accidentally persist a current-policy
// projection after a concurrent policy mutation.
func ValidateAuthorizationPolicyRevisionTx(ctx context.Context, tx Tx, scope access.AuthorizationPolicyScope, revision int64, digest string) (access.AuthorizationPolicy, error) {
	if tx == nil {
		return access.AuthorizationPolicy{}, errors.New("authorization policy PostgreSQL transaction is required")
	}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if revision <= 0 || !canonicalPolicyDigest(digest) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy revision and digest must be positive and canonical", access.ErrAuthorizationPolicyConflict)
	}
	row, err := accessdb.New(tx).LockAuthorizationPolicyHeadForShare(ctx, policyScopeParamsForShare(scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy head is absent", access.ErrAuthorizationPolicyNotFound)
	}
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("lock authorization policy head for share: %w", err)
	}
	if row.TargetID != scope.TargetID || row.ProjectID != scope.ProjectID || row.Environment != scope.Environment || row.Revision != revision || row.Digest != digest {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: expected revision=%d digest=%s, current revision=%d digest=%s", access.ErrAuthorizationPolicyStaleRevision, revision, digest, row.Revision, row.Digest)
	}
	return (&Repository{db: tx}).authorizationPolicyAtRevision(ctx, tx, scope, revision, digest)
}

type authorizationPolicyCommandWire struct {
	Scope            access.AuthorizationPolicyScope `json:"scope"`
	Binding          access.RoleBinding              `json:"binding"`
	ExpectedRevision int64                           `json:"expectedRevision"`
}

func authorizationPolicyCommandDigest(input access.AuthorizationRoleBindingInput) (string, error) {
	encoded, err := json.Marshal(authorizationPolicyCommandWire{Scope: input.Scope, Binding: input.Binding, ExpectedRevision: input.ExpectedRevision})
	if err != nil {
		return "", fmt.Errorf("encode authorization policy command: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalPolicyDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func cloneAuthorizationRoleBindings(input []access.RoleBinding) []access.RoleBinding {
	output := append([]access.RoleBinding(nil), input...)
	for i := range output {
		output[i].Capabilities = append([]access.Capability(nil), output[i].Capabilities...)
	}
	return output
}

func equalAuthorizationRoleBinding(left, right access.RoleBinding) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Subject != right.Subject || left.Role != right.Role || len(left.Capabilities) != len(right.Capabilities) {
		return false
	}
	for i := range left.Capabilities {
		if left.Capabilities[i] != right.Capabilities[i] {
			return false
		}
	}
	return true
}

func validateAuthorizationPolicySubject(ctx context.Context, db DBTX, subject access.SubjectRef) error {
	if err := subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %w", access.ErrAuthorizationPolicyInvalidBinding, err)
	}
	id, err := uuidID("authorization policy subject id", subject.ID)
	if err != nil {
		return fmt.Errorf("%w: subject: %w", access.ErrAuthorizationPolicyInvalidBinding, err)
	}
	queries := accessdb.New(db)
	var exists bool
	if subject.Kind == access.SubjectKindPrincipal {
		exists, err = queries.AuthorizationPolicyPrincipalExists(ctx, mustPGUUID(id))
	} else {
		exists, err = queries.AuthorizationPolicyGroupExists(ctx, mustPGUUID(id))
	}
	if err != nil {
		return fmt.Errorf("validate authorization policy subject: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: subject %s does not exist or is revoked", access.ErrAuthorizationPolicyInvalidBinding, id)
	}
	return nil
}

func staleAuthorizationPolicyRevisionError(expected, current int64) error {
	return fmt.Errorf("%w: %w: expected %d, current %d", access.ErrAuthorizationPolicyStaleRevision, access.ErrAuthorizationPolicyConflict, expected, current)
}

func conflictingAuthorizationPolicyIdempotencyError() error {
	return fmt.Errorf("%w: %w", access.ErrAuthorizationPolicyIdempotency, access.ErrAuthorizationPolicyConflict)
}

func (r *Repository) checkAuthorizationPolicyOperation(ctx context.Context, db DBTX, input access.AuthorizationRoleBindingInput, requestDigest string) (access.AuthorizationPolicy, bool, error) {
	row, err := accessdb.New(db).GetAuthorizationPolicyOperation(ctx, accessdb.GetAuthorizationPolicyOperationParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, IdempotencyKey: input.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, false, nil
	}
	if err != nil {
		return access.AuthorizationPolicy{}, false, fmt.Errorf("read authorization policy idempotency record: %w", err)
	}
	if row.RequestDigest != requestDigest {
		return access.AuthorizationPolicy{}, false, conflictingAuthorizationPolicyIdempotencyError()
	}
	policy, err := (&Repository{db: db}).authorizationPolicyAtRevision(ctx, db, input.Scope, row.Revision, row.PolicyDigest)
	if err != nil {
		return access.AuthorizationPolicy{}, false, err
	}
	return policy, true, nil
}

func insertAuthorizationPolicyRevision(ctx context.Context, db DBTX, input access.AuthorizationRoleBindingInput, revision int64, digest, requestDigest string, bindings []access.RoleBinding) (access.AuthorizationPolicy, error) {
	queries := accessdb.New(db)
	if err := queries.InsertAuthorizationPolicyRevision(ctx, accessdb.InsertAuthorizationPolicyRevisionParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: revision, Digest: digest}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("insert authorization policy revision: %w", err)
	}
	for _, binding := range bindings {
		encoded, marshalErr := json.Marshal(binding.Capabilities)
		if marshalErr != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("encode authorization policy role binding %q: %w", binding.ID, marshalErr)
		}
		if err := queries.InsertAuthorizationPolicyRoleBinding(ctx, accessdb.InsertAuthorizationPolicyRoleBindingParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: revision, ID: binding.ID, SubjectKind: string(binding.Subject.Kind), SubjectID: binding.Subject.ID, Role: string(binding.Role), Capabilities: encoded, Name: binding.Name}); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("insert authorization policy role binding %q: %w", binding.ID, err)
		}
	}
	if err := queries.InsertAuthorizationPolicyOperation(ctx, accessdb.InsertAuthorizationPolicyOperationParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, IdempotencyKey: input.IdempotencyKey, RequestDigest: requestDigest, Revision: revision, PolicyDigest: digest, BindingID: input.Binding.ID}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("record authorization policy idempotency: %w", err)
	}
	return (&Repository{db: db}).authorizationPolicyAtRevision(ctx, db, input.Scope, revision, digest)
}

func (r *Repository) upsertAuthorizationRoleBindingCore(ctx context.Context, db DBTX, input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	if ctx == nil {
		return access.AuthorizationPolicy{}, errors.New("authorization policy context is nil")
	}
	if err := r.validateAuthorizationPolicyScope(input.Scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if input.ExpectedRevision < 0 {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: expected revision cannot be negative", access.ErrAuthorizationPolicyConflict)
	}
	key, err := bounded(input.IdempotencyKey, "authorization policy idempotency key", maxAuthorizationPolicyIdempotencyKeyBytes)
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: %v", access.ErrAuthorizationPolicyIdempotency, err)
	}
	input.IdempotencyKey = key
	requestDigest, err := authorizationPolicyCommandDigest(input)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if replay, ok, replayErr := r.checkAuthorizationPolicyOperation(ctx, db, input, requestDigest); ok || replayErr != nil {
		return replay, replayErr
	}
	if err := access.ValidateAuthorizationRoleBinding(input.Binding); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if err := validateAuthorizationPolicySubject(ctx, db, input.Binding.Subject); err != nil {
		return access.AuthorizationPolicy{}, err
	}

	queries := accessdb.New(db)
	headRow, err := queries.LockAuthorizationPolicyHead(ctx, policyScopeLockParams(input.Scope))
	if errors.Is(err, pgx.ErrNoRows) {
		if input.ExpectedRevision != 0 {
			return access.AuthorizationPolicy{}, staleAuthorizationPolicyRevisionError(input.ExpectedRevision, 0)
		}
		initialBindings := []access.RoleBinding{input.Binding}
		initialDigest, digestErr := access.AuthorizationPolicyDigest(input.Scope, initialBindings)
		if digestErr != nil {
			return access.AuthorizationPolicy{}, digestErr
		}
		insertTag, insertErr := queries.InsertAuthorizationPolicyHead(ctx, accessdb.InsertAuthorizationPolicyHeadParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: 1, Digest: initialDigest})
		if insertErr != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("create authorization policy head: %w", insertErr)
		}
		if insertTag.RowsAffected() == 1 {
			return insertAuthorizationPolicyRevision(ctx, db, input, 1, initialDigest, requestDigest, initialBindings)
		}
		headRow, err = queries.LockAuthorizationPolicyHead(ctx, policyScopeLockParams(input.Scope))
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy head disappeared", access.ErrAuthorizationPolicyNotFound)
		}
		return access.AuthorizationPolicy{}, fmt.Errorf("lock authorization policy head: %w", err)
	}
	head := policyHeadFromLockRow(headRow)
	// A second caller with the same key can have waited on the head lock after
	// the first caller committed. Re-check idempotency after locking before CAS.
	if replay, ok, replayErr := r.checkAuthorizationPolicyOperation(ctx, db, input, requestDigest); ok || replayErr != nil {
		return replay, replayErr
	}
	if input.ExpectedRevision != head.Revision {
		return access.AuthorizationPolicy{}, staleAuthorizationPolicyRevisionError(input.ExpectedRevision, head.Revision)
	}
	current, err := r.authorizationPolicyAtRevision(ctx, db, input.Scope, head.Revision, head.Digest)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	bindings := current.RoleBindings
	replaced := false
	for index := range bindings {
		if bindings[index].ID != input.Binding.ID {
			if bindings[index].Subject == input.Binding.Subject && bindings[index].Role == input.Binding.Role {
				return access.AuthorizationPolicy{}, fmt.Errorf("%w: subject/role already bound by %q", access.ErrAuthorizationPolicyConflict, bindings[index].ID)
			}
			continue
		}
		if equalAuthorizationRoleBinding(bindings[index], input.Binding) {
			if err := queries.InsertAuthorizationPolicyOperation(ctx, accessdb.InsertAuthorizationPolicyOperationParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, IdempotencyKey: input.IdempotencyKey, RequestDigest: requestDigest, Revision: head.Revision, PolicyDigest: head.Digest, BindingID: input.Binding.ID}); err != nil {
				return access.AuthorizationPolicy{}, fmt.Errorf("record authorization policy idempotency: %w", err)
			}
			return current, nil
		}
		bindings[index] = input.Binding
		replaced = true
	}
	if !replaced {
		bindings = append(bindings, input.Binding)
	}
	digest, err := access.AuthorizationPolicyDigest(input.Scope, bindings)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	nextRevision := head.Revision + 1
	if err := queries.InsertAuthorizationPolicyRevision(ctx, accessdb.InsertAuthorizationPolicyRevisionParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: nextRevision, Digest: digest}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("insert authorization policy revision: %w", err)
	}
	for _, binding := range bindings {
		encoded, marshalErr := json.Marshal(binding.Capabilities)
		if marshalErr != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("encode authorization policy role binding %q: %w", binding.ID, marshalErr)
		}
		if err := queries.InsertAuthorizationPolicyRoleBinding(ctx, accessdb.InsertAuthorizationPolicyRoleBindingParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: nextRevision, ID: binding.ID, SubjectKind: string(binding.Subject.Kind), SubjectID: binding.Subject.ID, Role: string(binding.Role), Capabilities: encoded, Name: binding.Name}); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("insert authorization policy role binding %q: %w", binding.ID, err)
		}
	}
	updateTag, err := queries.UpdateAuthorizationPolicyHead(ctx, accessdb.UpdateAuthorizationPolicyHeadParams{Revision: nextRevision, Digest: digest, ExpectedRevision: head.Revision, TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment})
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("advance authorization policy revision: %w", err)
	}
	if updateTag.RowsAffected() != 1 {
		return access.AuthorizationPolicy{}, staleAuthorizationPolicyRevisionError(input.ExpectedRevision, head.Revision)
	}
	if err := queries.InsertAuthorizationPolicyOperation(ctx, accessdb.InsertAuthorizationPolicyOperationParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, IdempotencyKey: input.IdempotencyKey, RequestDigest: requestDigest, Revision: nextRevision, PolicyDigest: digest, BindingID: input.Binding.ID}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("record authorization policy idempotency: %w", err)
	}
	return (&Repository{db: db}).authorizationPolicyAtRevision(ctx, db, input.Scope, nextRevision, digest)
}

// UpsertAuthorizationRoleBinding applies one exact role binding through a
// target-scoped revision CAS. Replays with the same idempotency key and exact
// command return the original immutable revision without advancing state.
func (r *Repository) UpsertAuthorizationRoleBinding(ctx context.Context, input access.AuthorizationRoleBindingInput) (result access.AuthorizationPolicy, err error) {
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	result, err = r.upsertAuthorizationRoleBindingCore(ctx, tx, input)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("commit authorization policy upsert: %w", err)
		}
	}
	return result, nil
}

// UpsertAuthorizationRoleBindingTx composes the policy mutation into a
// caller-owned transaction, preserving the same CAS/idempotency boundary.
func UpsertAuthorizationRoleBindingTx(ctx context.Context, tx Tx, input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	if tx == nil {
		return access.AuthorizationPolicy{}, errors.New("authorization policy PostgreSQL transaction is required")
	}
	return (&Repository{db: tx}).upsertAuthorizationRoleBindingCore(ctx, tx, input)
}
