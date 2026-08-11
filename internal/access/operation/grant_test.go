package operation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type grantRepository struct {
	access.Repository
	grants   map[string]access.Grant
	audits   []access.AuditEventInput
	auditErr error
}

type nonTransactionalGrantRepository struct {
	access.Repository
	mutations int
}

func (r *nonTransactionalGrantRepository) CreateGrant(_ context.Context, _ access.GrantInput) (access.Grant, error) {
	r.mutations++
	return access.Grant{ID: "unexpected"}, nil
}

func (r *grantRepository) CreateGrant(_ context.Context, input access.GrantInput) (access.Grant, error) {
	row := access.Grant{
		ID: "grant-1", ObjectID: input.Object.ObjectID, ObjectType: input.Object.Type,
		WorkspaceID: input.Object.WorkspaceID, SubjectType: input.SubjectType,
		SubjectID: input.SubjectID, Privilege: input.Privilege,
	}
	r.grants[row.ID] = row
	return row, nil
}

func (r *grantRepository) GetGrant(_ context.Context, _, id string) (access.Grant, error) {
	return r.grants[id], nil
}

func (r *grantRepository) UpdateGrant(_ context.Context, workspaceID, id string, input access.GrantInput) (access.Grant, error) {
	row := r.grants[id]
	row.ObjectID = input.Object.ObjectID
	row.ObjectType = input.Object.Type
	row.WorkspaceID = workspaceID
	row.SubjectType = input.SubjectType
	row.SubjectID = input.SubjectID
	row.Privilege = input.Privilege
	r.grants[id] = row
	return row, nil
}

func (r *grantRepository) DeleteGrant(_ context.Context, _, id string) error {
	delete(r.grants, id)
	return nil
}

func (r *grantRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	before := make(map[string]access.Grant, len(r.grants))
	for id, grant := range r.grants {
		before[id] = grant
	}
	input, err := mutation(r)
	if err == nil && r.auditErr != nil {
		r.grants = before
		return r.auditErr
	}
	if err == nil {
		r.audits = append(r.audits, input)
	}
	return err
}

func TestGrantCommandsEnforceGeneratedSurfaceAndAuditContract(t *testing.T) {
	repo := &grantRepository{grants: map[string]access.Grant{}}
	commands, err := NewGrantCommands(func() (access.Repository, error) { return repo, nil }, grantTestCatalog(t))
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	invocation := access.GrantInvocation{
		PrincipalID: "principal-admin", Surface: access.OperationSurfaceUI,
		RequestID: "request-1", CorrelationID: "correlation-1",
	}
	input := access.GrantInput{
		Object:      access.ObjectRef{Type: access.SecurableDashboard, WorkspaceID: "sales", ObjectID: "executive"},
		SubjectType: access.SubjectPrincipal, SubjectID: "principal-viewer", Privilege: access.PrivilegeViewItem,
	}

	created, err := commands.CreateGrant(t.Context(), invocation, input)
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	assertGrantAudit(t, repo.audits[0], access.OperationCreateGrant, "grant.created", access.OperationSurfaceUI, "sales")

	input.Privilege = access.PrivilegeQueryData
	if _, err := commands.UpdateGrant(t.Context(), invocation, "sales", created.ID, input); err == nil {
		t.Fatal("UI invocation bypassed the generated updateGrant exposure contract")
	}
	if repo.grants[created.ID].Privilege != access.PrivilegeViewItem || len(repo.audits) != 1 {
		t.Fatalf("rejected update mutated state: grants=%#v audits=%#v", repo.grants, repo.audits)
	}

	invocation.Surface = access.OperationSurfaceAPI
	updated, err := commands.UpdateGrant(t.Context(), invocation, "sales", created.ID, input)
	if err != nil {
		t.Fatalf("update grant: %v", err)
	}
	if updated.Privilege != access.PrivilegeQueryData {
		t.Fatalf("updated grant = %#v", updated)
	}
	assertGrantAudit(t, repo.audits[1], access.OperationUpdateGrant, "grant.updated", access.OperationSurfaceAPI, "sales")

	invocation.Surface = access.OperationSurfaceUI
	deleted, err := commands.DeleteGrant(t.Context(), invocation, "sales", created.ID)
	if err != nil {
		t.Fatalf("delete grant: %v", err)
	}
	if deleted.ID != created.ID || len(repo.grants) != 0 {
		t.Fatalf("deleted grant = %#v remaining = %#v", deleted, repo.grants)
	}
	assertGrantAudit(t, repo.audits[2], access.OperationDeleteGrant, "grant.deleted", access.OperationSurfaceUI, "sales")
}

func TestGrantCommandsRollBackMutationWhenRequiredAuditFails(t *testing.T) {
	repo := &grantRepository{grants: map[string]access.Grant{}, auditErr: errors.New("audit unavailable")}
	commands, err := NewGrantCommands(func() (access.Repository, error) { return repo, nil }, grantTestCatalog(t))
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	_, err = commands.CreateGrant(t.Context(), access.GrantInvocation{Surface: access.OperationSurfaceAPI}, access.GrantInput{
		Object: access.WorkspaceObject("sales"), SubjectType: access.SubjectPrincipal,
		SubjectID: "principal-viewer", Privilege: access.PrivilegeViewItem,
	})
	if err == nil {
		t.Fatal("expected audit persistence failure")
	}
	if len(repo.grants) != 0 {
		t.Fatalf("mutation was not rolled back: %#v", repo.grants)
	}
}

func TestGrantCommandsRejectMissingTransactionBeforeMutation(t *testing.T) {
	repo := &nonTransactionalGrantRepository{}
	commands, err := NewGrantCommands(func() (access.Repository, error) { return repo, nil }, grantTestCatalog(t))
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	_, err = commands.CreateGrant(t.Context(), access.GrantInvocation{Surface: access.OperationSurfaceAPI}, access.GrantInput{})
	if !errors.Is(err, access.ErrAuditTransaction) {
		t.Fatalf("create error = %v, want %v", err, access.ErrAuditTransaction)
	}
	if repo.mutations != 0 {
		t.Fatalf("mutations = %d, want zero", repo.mutations)
	}
}

func TestGrantAuditKeepsProjectEnvironmentAtPlatformScope(t *testing.T) {
	descriptor, _ := grantTestCatalog(t).DescribeOperation(access.OperationCreateGrant)
	input, err := grantAuditInput(descriptor, access.GrantInvocation{Surface: access.OperationSurfaceAPI}, access.Grant{
		ID: "grant-1", WorkspaceID: "finance", ObjectType: access.SecurableProjectEnvironment,
		ObjectID: "production", SubjectType: access.SubjectPrincipal,
		SubjectID: "reviewer", Privilege: access.PrivilegeApproveDeployment,
	})
	if err != nil {
		t.Fatalf("encode grant audit: %v", err)
	}
	if input.WorkspaceID != "" {
		t.Fatalf("audit workspace = %q, want platform scope", input.WorkspaceID)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(input.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["projectId"] != "finance" || payload["environment"] != "production" {
		t.Fatalf("audit metadata = %#v", metadata)
	}
}

func grantTestCatalog(t *testing.T) access.OperationCatalog {
	t.Helper()
	descriptors := []access.OperationDescriptor{
		{
			ID: access.OperationCreateGrant, Kind: access.OperationKindCommand, Owner: "LeapViewAPI.Access",
			Target:    access.OperationTarget{Type: access.SecurableWorkspace, Parameter: "workspace"},
			Privilege: access.PrivilegeManageGrants, AuditEvent: "grant.created", HTTPIdempotency: "required",
			ExposedSurfaces: []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceCLI, access.OperationSurfaceUI},
		},
		{
			ID: access.OperationUpdateGrant, Kind: access.OperationKindCommand, Owner: "LeapViewAPI.Access",
			Target:    access.OperationTarget{Type: access.SecurableWorkspace, Parameter: "workspace"},
			Privilege: access.PrivilegeManageGrants, AuditEvent: "grant.updated", HTTPConcurrency: "if-match",
			ExposedSurfaces: []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceCLI},
		},
		{
			ID: access.OperationDeleteGrant, Kind: access.OperationKindCommand, Owner: "LeapViewAPI.Access",
			Target:    access.OperationTarget{Type: access.SecurableWorkspace, Parameter: "workspace"},
			Privilege: access.PrivilegeManageGrants, AuditEvent: "grant.deleted",
			ExposedSurfaces: []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceCLI, access.OperationSurfaceUI},
		},
	}
	catalog, err := access.NewOperationCatalog(descriptors)
	if err != nil {
		t.Fatalf("build operation catalog: %v", err)
	}
	return catalog
}

func assertGrantAudit(t *testing.T, input access.AuditEventInput, operation access.OperationID, action string, surface access.OperationSurface, workspaceID string) {
	t.Helper()
	if input.Action != action || input.WorkspaceID != workspaceID || input.PrincipalID != "principal-admin" || input.Privilege != access.PrivilegeManageGrants || input.RequestID != "request-1" || input.CorrelationID != "correlation-1" {
		t.Fatalf("audit input = %#v", input)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(input.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["operationId"] != string(operation) || payload["surface"] != string(surface) || payload["privilege"] == "" {
		t.Fatalf("audit metadata = %#v", metadata)
	}
}
