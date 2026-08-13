package operation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

type roleBindingRepository struct {
	access.Repository
	bindings map[string]access.RoleBinding
	audits   []access.AuditEventInput
	auditErr error
}

type nonTransactionalRoleBindingRepository struct {
	access.Repository
	mutations int
}

func TestCreateRoleBindingAuditPayloadSensitivityIsGenerated(t *testing.T) {
	payload := accessgen.GenSchemaRoleBindingCreatedAuditPayload{
		OperationId: "createRoleBinding",
		Role:        "viewer",
		SubjectId:   "principal-sensitive",
		SubjectType: "principal",
		Surface:     "ui",
	}
	encoded, err := accessgen.EncodeGenCreateRoleBindingAuditPayloadForLog(payload)
	if err != nil {
		t.Fatalf("encode safe audit log: %v", err)
	}
	if strings.Contains(encoded, payload.OperationId) || strings.Contains(encoded, payload.Role) || strings.Contains(encoded, payload.SubjectId) || strings.Contains(encoded, payload.SubjectType) {
		t.Fatalf("safe audit log leaked classified data: %s", encoded)
	}
	if !strings.Contains(encoded, `"surface":"ui"`) || !strings.Contains(encoded, `"subjectId":"[REDACTED]"`) {
		t.Fatalf("safe audit log = %s", encoded)
	}
	contract, ok := accessgen.GetAPIGenCommandRuntimeContract("createRoleBinding")
	if !ok || contract.AuditPayload == nil || contract.AuditPayload.SchemaVersion != 1 || contract.AuditPayload.Retention != "security" {
		t.Fatalf("generated audit contract = %#v, %t", contract.AuditPayload, ok)
	}
}

func (r *nonTransactionalRoleBindingRepository) CreateRoleBinding(_ context.Context, _ access.RoleBindingInput) (access.RoleBinding, error) {
	r.mutations++
	return access.RoleBinding{ID: "unexpected"}, nil
}

func (r *roleBindingRepository) CreateRoleBinding(_ context.Context, input access.RoleBindingInput) (access.RoleBinding, error) {
	row := access.RoleBinding{
		ID: "binding-1", WorkspaceID: input.WorkspaceID,
		SubjectType: input.SubjectType, SubjectID: input.SubjectID, Role: input.Role,
	}
	r.bindings[row.ID] = row
	return row, nil
}

func (r *roleBindingRepository) GetRoleBinding(_ context.Context, workspaceID, id string) (access.RoleBinding, error) {
	return r.bindings[id], nil
}

func (r *roleBindingRepository) UpdateRoleBinding(_ context.Context, workspaceID, id string, input access.RoleBindingInput) (access.RoleBinding, error) {
	row := r.bindings[id]
	row.WorkspaceID = workspaceID
	row.SubjectType = input.SubjectType
	row.SubjectID = input.SubjectID
	row.Role = input.Role
	r.bindings[id] = row
	return row, nil
}

func (r *roleBindingRepository) DeleteRoleBinding(_ context.Context, _, id string) error {
	delete(r.bindings, id)
	return nil
}

func (r *roleBindingRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	before := make(map[string]access.RoleBinding, len(r.bindings))
	for id, binding := range r.bindings {
		before[id] = binding
	}
	input, err := mutation(r)
	if err == nil && r.auditErr != nil {
		r.bindings = before
		return r.auditErr
	}
	if err == nil {
		r.audits = append(r.audits, input)
	}
	return err
}

func TestRoleBindingCommandsRollbackWhenAuditPersistenceFails(t *testing.T) {
	repo := &roleBindingRepository{bindings: map[string]access.RoleBinding{}, auditErr: errors.New("audit unavailable")}
	commands, err := NewRoleBindingCommands(func() (access.Repository, error) { return repo, nil })
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	_, err = commands.CreateRoleBinding(t.Context(), access.RoleBindingInvocation{Surface: access.OperationSurfaceAPI}, access.RoleBindingInput{WorkspaceID: "sales"})
	if err == nil {
		t.Fatal("expected audit persistence failure")
	}
	if len(repo.bindings) != 0 {
		t.Fatalf("mutation was not rolled back: %#v", repo.bindings)
	}
}

func TestRoleBindingCommandsRejectMissingTransactionBeforeMutation(t *testing.T) {
	repo := &nonTransactionalRoleBindingRepository{}
	commands, err := NewRoleBindingCommands(func() (access.Repository, error) { return repo, nil })
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	_, err = commands.CreateRoleBinding(t.Context(), access.RoleBindingInvocation{Surface: access.OperationSurfaceAPI}, access.RoleBindingInput{})
	if !errors.Is(err, access.ErrAuditTransaction) {
		t.Fatalf("create error = %v, want %v", err, access.ErrAuditTransaction)
	}
	if repo.mutations != 0 {
		t.Fatalf("mutations = %d, want zero", repo.mutations)
	}
}

func TestRoleBindingCommandsShareStableOperationContract(t *testing.T) {
	repo := &roleBindingRepository{bindings: map[string]access.RoleBinding{}}
	commands, err := NewRoleBindingCommands(func() (access.Repository, error) { return repo, nil })
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	invocation := access.RoleBindingInvocation{
		PrincipalID: "principal-admin", Surface: access.OperationSurfaceUI,
		RequestID: "request-1", CorrelationID: "correlation-1", IdempotencyKey: "role-binding-1",
		OperationClaims: []string{accessgen.GenCommandOperationCreateRoleBinding().APIGenOperationID()},
	}

	created, err := commands.CreateRoleBinding(t.Context(), invocation, access.RoleBindingInput{
		WorkspaceID: "sales", SubjectType: access.SubjectPrincipal,
		SubjectID: "principal-viewer", Role: access.RoleViewer,
	})
	if err != nil {
		t.Fatalf("create role binding: %v", err)
	}
	if created.ID != "binding-1" {
		t.Fatalf("created binding = %#v", created)
	}
	assertRoleBindingAudit(t, repo.audits[0], accessgen.GenCommandOperationCreateRoleBinding().APIGenOperationID(), "role_binding.created", access.OperationSurfaceUI)

	invocation.Surface = access.OperationSurfaceAPI
	invocation.ConcurrencyToken, err = access.RoleBindingRevision(created)
	if err != nil {
		t.Fatalf("role binding revision: %v", err)
	}
	updated, err := commands.UpdateRoleBinding(t.Context(), invocation, "sales", created.ID, access.RoleBindingInput{
		WorkspaceID: "sales", SubjectType: access.SubjectPrincipal,
		SubjectID: "principal-viewer", Role: access.RoleEditor,
	})
	if err != nil {
		t.Fatalf("update role binding: %v", err)
	}
	if updated.Role != access.RoleEditor {
		t.Fatalf("updated binding = %#v", updated)
	}
	assertRoleBindingAudit(t, repo.audits[1], accessgen.GenCommandOperationUpdateRoleBinding().APIGenOperationID(), "role_binding.updated", access.OperationSurfaceAPI)

	deleted, err := commands.DeleteRoleBinding(t.Context(), invocation, "sales", created.ID)
	if err != nil {
		t.Fatalf("delete role binding: %v", err)
	}
	if deleted.ID != created.ID || len(repo.bindings) != 0 {
		t.Fatalf("deleted binding = %#v remaining = %#v", deleted, repo.bindings)
	}
	assertRoleBindingAudit(t, repo.audits[2], accessgen.GenCommandOperationDeleteRoleBinding().APIGenOperationID(), "role_binding.deleted", access.OperationSurfaceAPI)
}

func TestRoleBindingUpdateRejectsStaleRevisionInsideAuditTransaction(t *testing.T) {
	repo := &roleBindingRepository{bindings: map[string]access.RoleBinding{
		"binding-1": {ID: "binding-1", WorkspaceID: "sales", SubjectType: access.SubjectPrincipal, SubjectID: "viewer", Role: access.RoleViewer},
	}}
	commands, err := NewRoleBindingCommands(func() (access.Repository, error) { return repo, nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = commands.UpdateRoleBinding(t.Context(), access.RoleBindingInvocation{
		Surface: access.OperationSurfaceAPI, ConcurrencyToken: `"stale"`,
	}, "sales", "binding-1", access.RoleBindingInput{WorkspaceID: "sales", SubjectType: access.SubjectPrincipal, SubjectID: "viewer", Role: access.RoleEditor})
	if !errors.Is(err, apigencommand.ErrPreconditionFailed) {
		t.Fatalf("update error = %v", err)
	}
	if got := repo.bindings["binding-1"].Role; got != access.RoleViewer {
		t.Fatalf("stale update committed role %q", got)
	}
	if len(repo.audits) != 0 {
		t.Fatalf("stale update wrote audit: %#v", repo.audits)
	}
}

func TestRoleBindingGeneratedContractsAreTransportNeutral(t *testing.T) {
	for _, id := range []string{
		accessgen.GenCommandOperationCreateRoleBinding().APIGenOperationID(),
		accessgen.GenCommandOperationUpdateRoleBinding().APIGenOperationID(),
		accessgen.GenCommandOperationDeleteRoleBinding().APIGenOperationID(),
	} {
		descriptor, ok := accessgen.GetAPIGenCommandRuntimeContract(id)
		if !ok {
			t.Fatalf("operation %q is not registered", id)
		}
		if descriptor.Owner != "LeapViewAPI.Access" || descriptor.Target == nil || descriptor.Target.Type != string(access.SecurableWorkspace) {
			t.Fatalf("operation %q descriptor = %#v", id, descriptor)
		}
		for _, surface := range []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceCLI, access.OperationSurfaceUI} {
			if !descriptor.Exposes(surface) {
				t.Errorf("operation %q is not exposed to %q", id, surface)
			}
		}
	}
}

func assertRoleBindingAudit(t *testing.T, input access.AuditEventInput, operation string, action string, surface access.OperationSurface) {
	t.Helper()
	if input.Action != action || input.WorkspaceID != "sales" || input.PrincipalID != "principal-admin" || input.RequestID != "request-1" || input.CorrelationID != "correlation-1" {
		t.Fatalf("audit input = %#v", input)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(input.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	wantSchema := "RoleBindingAuditPayload"
	if operation == accessgen.GenCommandOperationCreateRoleBinding().APIGenOperationID() {
		wantSchema = "RoleBindingCreatedAuditPayload"
	}
	if metadata["schemaVersion"] != float64(1) || metadata["retention"] != "security" || metadata["payloadSchema"] != wantSchema {
		t.Fatalf("audit envelope = %#v", metadata)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["operationId"] != operation || payload["surface"] != string(surface) || payload["subjectId"] != "principal-viewer" {
		t.Fatalf("audit payload = %#v", metadata["payload"])
	}
}
