package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type sourceCredentialTestAudit struct{}

func (sourceCredentialTestAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}

type sourceCredentialInterruptedReader struct {
	deploymentmodule.NativeDeliveryReader
	beforeRead func()
	failure    error
}

func (r sourceCredentialInterruptedReader) LoadGeneration(context.Context, string) (deploymentpostgres.DeliveryGeneration, error) {
	r.beforeRead()
	return deploymentpostgres.DeliveryGeneration{}, r.failure
}

func TestSourceCredentialRequestSurvivesInterruptionBeforeNativeBuild(t *testing.T) {
	db := authorizationPolicyUpgradeDB(t)
	seedAuthorizationPolicyUpgradeFixture(t, db)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = credentialpostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	repository, err := credentialpostgres.New(db, sourceCredentialTestAudit{})
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.NewString()
	version := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	binding := encryption.Binding{VersionID: version, DeploymentID: authorizationPolicyUpgradeTargetID, OwnerID: "customer", ScopeKind: "connection", TargetID: authorizationPolicyUpgradeTargetID, ProjectID: authorizationPolicyUpgradeProjectID, Environment: authorizationPolicyUpgradeEnvironment, ResourceID: "connection:source", Provider: "postgres", Purpose: "connection-authentication", Destination: "sha256:" + strings.Repeat("a", 64)}
	if _, err = db.Exec(t.Context(), `INSERT INTO credential.draft_version(version_id,deployment_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, binding.VersionID, binding.DeploymentID, binding.OwnerID, binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment, binding.ResourceID, binding.Purpose, binding.Provider, binding.Destination, actor, now); err != nil {
		t.Fatal(err)
	}
	receipt := credential.ValidationReceipt{ReceiptID: uuid.NewString(), Binding: binding, ActorID: actor, BindingID: "source-binding", BindingRevision: 1, ConfigurationDigest: "sha256:" + strings.Repeat("b", 64), ValidatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	audit, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	request := credential.ActivationRequest{OperationID: uuid.NewString(), VersionID: version, ReceiptID: receipt.ReceiptID, ExpectedBindingRevision: 1}
	resource := credential.Resource{ScopeKind: binding.ScopeKind, TargetID: binding.TargetID, ProjectID: binding.ProjectID, Environment: binding.Environment, ResourceID: binding.ResourceID}
	delivery := deploymentpostgres.New(db)
	injected := errors.New("interrupted before native build")
	source := &sourceCredentialActivation{config: sourceCredentialConfig{Pool: db, Credentials: repository, TargetID: binding.TargetID, Environment: binding.Environment, Delivery: delivery, Authorize: func(context.Context, string, credential.Resource) error { return nil }, AuthorizeTx: func(ctx context.Context, tx pgx.Tx, _ string, _ credential.ValidationReceipt) error {
		_, err := delivery.TargetForUpdateTx(ctx, tx, binding.TargetID)
		return err
	}}}
	reached := false
	source.config.Reader = sourceCredentialInterruptedReader{failure: injected, beforeRead: func() {
		reached = true
		row, err := repository.GetActivationRequest(t.Context(), binding.TargetID, request.OperationID)
		if err != nil || row.State != "preparing" || row.Request != request {
			t.Fatalf("native work preceded durable reservation: %#v %v", row, err)
		}
	}}
	result, err := source.Prepare(t.Context(), actor, resource, request)
	if !errors.Is(err, injected) || !reached || result.Status.State != "preparing" {
		t.Fatalf("interrupted prepare: %#v %v reached=%v", result, err, reached)
	}
	// A new process recovers exactly the original operation and cannot substitute
	// another actor/version or silently clear ordinary publication's fence.
	restarted := &sourceCredentialActivation{config: source.config}
	recovered, err := restarted.Read(t.Context(), actor, resource, request.OperationID)
	if err != nil || recovered != result {
		t.Fatalf("recovered request: %#v %v", recovered, err)
	}
	if _, err = restarted.Read(t.Context(), "another-actor", resource, request.OperationID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("foreign actor read: %v", err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = credentialpostgres.CheckNoPendingActivationTx(t.Context(), tx, binding.TargetID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("interrupted operation lost fence: %v", err)
	}
	_ = tx.Rollback(t.Context())
	aborted, err := restarted.Abort(t.Context(), actor, recovered)
	if err != nil || aborted.Status.State != "aborted" {
		t.Fatalf("abort durable partial work: %#v %v", aborted, err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = credentialpostgres.CheckNoPendingActivationTx(t.Context(), tx, binding.TargetID); err != nil {
		t.Fatalf("aborted operation retained fence: %v", err)
	}
}
