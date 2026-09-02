package module

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type deploymentDBStub struct{}

func (deploymentDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (deploymentDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (deploymentDBStub) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (deploymentDBStub) Begin(context.Context) (pgx.Tx, error)                   { return nil, nil }

type readOnlyDeploymentDBStub struct{}

func (readOnlyDeploymentDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (readOnlyDeploymentDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (readOnlyDeploymentDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type activationAuditStub struct{}

func (activationAuditStub) AppendActivationAudit(context.Context, postgres.Tx, postgres.ActivationAuditInput) (postgres.AuditEvent, error) {
	return postgres.AuditEvent{}, nil
}

type nativeEventStub struct{}

func (nativeEventStub) AppendDeliveryEvent(context.Context, postgres.Tx, NativeDeliveryEventInput) (postgres.Event, error) {
	return postgres.Event{}, nil
}

type nativeAuditStub struct{}

func (nativeAuditStub) AppendMutationAudit(context.Context, postgres.Tx, NativeDeliveryAuditInput) (postgres.AuditEvent, error) {
	return postgres.AuditEvent{}, nil
}

type nativeWorkflowStub struct{}

func (nativeWorkflowStub) RecordWorkflow(context.Context, postgres.Tx, jobs.WorkflowIntent) error {
	return nil
}

type nativeOperationStub struct{}

func (nativeOperationStub) AcquireTx(context.Context, NativeOperationTx, NativeOperationAcquireInput) (NativeOperationAcquireResult, error) {
	return NativeOperationAcquireResult{}, nil
}
func (nativeOperationStub) CompleteTx(context.Context, NativeOperationTx, NativeOperationLease, json.RawMessage) error {
	return nil
}
func (activationAuditStub) GetActivationAudit(context.Context, postgres.Tx, postgres.ActivationAuditInput) (postgres.AuditEvent, error) {
	return postgres.AuditEvent{}, nil
}

type approvalAppenderStub struct{}

func (approvalAppenderStub) AppendApprovalOperation(context.Context, postgres.Tx, postgres.ApprovalOperation) error {
	return nil
}
func (approvalAppenderStub) AppendApprovalEvent(context.Context, postgres.Tx, postgres.ApprovalEvent) error {
	return nil
}
func (approvalAppenderStub) AppendApprovalAudit(context.Context, postgres.Tx, postgres.ApprovalAudit) error {
	return nil
}
func (approvalAppenderStub) EnqueueApprovalActivation(context.Context, postgres.Tx, postgres.ApprovalRequest, postgres.ApprovalDecision) error {
	return nil
}

func testApprovalAuthority(repository *postgres.Repository) (*postgres.ApprovalAuthority, error) {
	return postgres.NewApprovalAuthority(repository, postgres.ApprovalAuthorityOptions{
		Authorize: postgres.ApprovalAuthorizerFunc(func(context.Context, postgres.ApprovalAuthorizationInput) error { return nil }),
		Operation: approvalAppenderStub{}, Event: approvalAppenderStub{}, Audit: approvalAppenderStub{}, Activation: approvalAppenderStub{},
	})
}

func TestNewPostgresPersistenceWiresNativeSurfaces(t *testing.T) {
	repository := postgres.NewWithOptions(deploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	persistence.Approval, err = testApprovalAuthority(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.validate(); err != nil {
		t.Fatal(err)
	}
	if persistence.Repository != repository || persistence.Candidates == nil || persistence.ProjectClaims == nil || persistence.DeliveryReader == nil || persistence.Activation == nil {
		t.Fatalf("native surfaces were not wired: %#v", persistence)
	}
}

func TestNewPostgresPersistenceRejectsNil(t *testing.T) {
	if _, err := NewPostgresPersistence(nil); err == nil {
		t.Fatal("expected nil repository rejection")
	}
}

func TestNewPostgresPersistenceRejectsNonTransactionalHandle(t *testing.T) {
	repository := postgres.NewWithOptions(readOnlyDeploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	if _, err := NewPostgresPersistence(repository); err == nil {
		t.Fatal("expected non-transactional PostgreSQL handle rejection")
	}
}

func TestBuildProductionNativePersistenceExposesModule(t *testing.T) {
	repository := postgres.NewWithOptions(deploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	persistence.Approval, err = testApprovalAuthority(repository)
	if err != nil {
		t.Fatal(err)
	}
	nativeMutations := NativeDeliveryMutationFuncs{
		Plan: func(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
			return NativeDeliveryPlan{}, nil
		},
		Build: func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
			return NativeDeliveryBuild{}, nil
		},
	}
	m, err := Build(t.Context(), Config{Persistence: &persistence, Production: true, InstanceID: "target", InstanceEnvironment: "prod", NativeDeliveryEvents: nativeEventStub{}, NativeDeliveryAudit: nativeAuditStub{}, NativeDeliveryWorkflow: nativeWorkflowStub{}, NativeOperationAuthority: nativeOperationStub{}, NativeDeliveryMutations: nativeMutations, API: APIConfig{Releases: &publishReleaseStub{}}})
	if err != nil {
		t.Fatal(err)
	}
	if m.NativePersistence() != &persistence {
		t.Fatal("module did not expose native persistence")
	}
	if m.projectClaims == nil {
		t.Fatal("module did not construct the native project-claim service")
	}
	if _, ok := m.jobs.Coordinator.(*nativeCoordinator); !ok {
		t.Fatalf("built production coordinator has type %T, want native coordinator", m.jobs.Coordinator)
	}
	if m.api.Releases != nil || m.deliveryMutations != nil || m.deliveryReader != nil {
		t.Fatalf("native module retained legacy delivery seams: api releases=%T mutations=%T reader=%T", m.api.Releases, m.deliveryMutations, m.deliveryReader)
	}
}

func TestBuildProductionNativePersistenceRequiresMutationAuthority(t *testing.T) {
	repository := postgres.NewWithOptions(deploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	persistence.Approval, err = testApprovalAuthority(repository)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		Persistence:          &persistence,
		Production:           true,
		InstanceID:           "target",
		InstanceEnvironment:  "prod",
		NativeDeliveryEvents: nativeEventStub{}, NativeDeliveryAudit: nativeAuditStub{},
		NativeDeliveryWorkflow: nativeWorkflowStub{}, NativeOperationAuthority: nativeOperationStub{},
	}
	if _, err := Build(t.Context(), config); err == nil {
		t.Fatal("expected missing native delivery mutation authority rejection")
	}
}

func TestBuildProductionNativePersistenceRejectsMissingMutationAuthority(t *testing.T) {
	repository := postgres.NewWithOptions(deploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	base := Config{Persistence: &persistence, Production: true, InstanceID: "target", InstanceEnvironment: "prod", NativeDeliveryEvents: nativeEventStub{}, NativeDeliveryAudit: nativeAuditStub{}, NativeDeliveryWorkflow: nativeWorkflowStub{}, NativeOperationAuthority: nativeOperationStub{}, NativeDeliveryMutations: NativeDeliveryMutationFuncs{Plan: func(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
		return NativeDeliveryPlan{}, nil
	}, Build: func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
		return NativeDeliveryBuild{}, nil
	}}}
	checks := []struct {
		name   string
		mutate func(*Config)
	}{
		{"events", func(c *Config) { c.NativeDeliveryEvents = nil }},
		{"audit", func(c *Config) { c.NativeDeliveryAudit = nil }},
		{"workflow", func(c *Config) { c.NativeDeliveryWorkflow = nil }},
		{"operations", func(c *Config) { c.NativeOperationAuthority = nil }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			config := base
			check.mutate(&config)
			if _, err := Build(t.Context(), config); err == nil {
				t.Fatal("expected missing native mutation authority rejection")
			}
		})
	}
}

func TestNativeOperationDispositionRejectsTamperedProjection(t *testing.T) {
	input := NativeOperationAcquireInput{Scope: "target", OperationType: "deployment.create", IdempotencyKey: "key", RequestDigest: "sha256:" + "aabbccdd" + "aabbccdd" + "aabbccdd" + "aabbccdd" + "aabbccdd" + "aabbccdd" + "aabbccdd" + "aabbccdd", OwnerID: "actor"}
	base := NativeOperationAcquireResult{Status: NativeOperationAcquired, Operation: NativeOperationRecord{Scope: input.Scope, OperationType: input.OperationType, IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest, OwnerID: input.OwnerID, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000001"}, Lease: NativeOperationLease{Scope: input.Scope, IdempotencyKey: input.IdempotencyKey, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000001", OwnerID: input.OwnerID, FencingGeneration: 1, LeaseExpiresAt: time.Now().Add(time.Minute)}}
	cases := []struct {
		name   string
		mutate func(*NativeOperationAcquireResult)
	}{
		{"operation id", func(r *NativeOperationAcquireResult) { r.Operation.OperationID = "not-a-uuid" }},
		{"lease scope", func(r *NativeOperationAcquireResult) { r.Lease.Scope = "other" }},
		{"lease fence", func(r *NativeOperationAcquireResult) { r.Lease.FencingGeneration = 0 }},
		{"lease expiry", func(r *NativeOperationAcquireResult) { r.Lease.LeaseExpiresAt = time.Time{} }},
		{"replay outcome", func(r *NativeOperationAcquireResult) { r.Status = NativeOperationReplay }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := base
			tc.mutate(&result)
			if _, err := nativeOperationDisposition(result, input); err == nil {
				t.Fatal("tampered operation projection was accepted")
			}
		})
	}
}
