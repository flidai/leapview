package adminpostgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	bindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deliverypostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

type firstSourceFixture struct {
	db          *pgxpool.Pool
	ops         Operations
	cfg         config.Config
	request     admincli.FirstSourceAdmissionRequest
	repo        *accesspostgres.Repository
	policy      access.AuthorizationPolicy
	maintenance *pgxpool.Pool
}

func newFirstSourceFixture(t *testing.T) *firstSourceFixture {
	t.Helper()
	h := postgrestest.Start(t)
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "first-source-maintenance-test"})
	database := h.NewDatabase(t, "first_source_admission")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	maintenance, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenance.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for _, apply := range []func(context.Context, credentialpostgres.Tx) error{platformbootstrap.ApplySchema, accesspostgres.ApplySchema, bindingpostgres.ApplySchema, credentialpostgres.ApplySchema, deliverypostgres.ApplySchema} {
		if err := apply(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := platformbootstrap.New(tx).EnsureInstanceID(t.Context(), credentialSetupInstance); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	bootstrap := platformbootstrap.New(db)
	if err := bootstrap.BindInstanceEnvironment(t.Context(), "prod"); err != nil {
		t.Fatal(err)
	}
	audit := firstSourceAudit{repository: accesspostgres.New()}
	if err := declareCustomerOwner(t.Context(), db, credentialSetupInstance, "customer:one", audit.RecordAuditEvent); err != nil {
		t.Fatal(err)
	}
	repo, err := accesspostgres.NewAccess(db, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "first-source-owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "first-source-operator@example.com", DisplayName: "Operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.ClaimProject(t.Context(), platformbootstrap.ProjectClaimInput{ProjectID: "finance", Environment: "prod", ClaimedBy: owner.Principal.ID, ClaimedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	role, err := access.NewTypedRoleBinding("owner", "Owner", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: owner.Principal.ID}, access.PermissionRoleProjectAdmin, "finance")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: access.AuthorizationPolicyScope{TargetID: credentialSetupInstance, ProjectID: "finance", Environment: "prod"}, Binding: role, IdempotencyKey: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := productionAdminConfig(root)
	cfg.CredentialKeyringFile = filepath.Join(root, "keyring.json")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":"key-1","keys":[{"key_id":"key-1","key_base64":%q,"state":"active_write"}]}`, credentialSetupInstance, base64.StdEncoding.EncodeToString(key))
	if err := os.WriteFile(cfg.CredentialKeyringFile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := &firstSourceFixture{db: db, cfg: cfg, repo: repo, policy: policy, maintenance: maintenance}
	fixture.ops = New(Dependencies{
		LoadConfig:     func() (config.Config, error) { return fixture.cfg, nil },
		AcquireLock:    func(string) (adminoffline.Lock, error) { return &testAdminLock{}, nil },
		OpenAccess:     func(context.Context, platformpostgres.Config) (AccessPool, error) { return customerSetupPool{db}, nil },
		VerifyBaseline: func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewAccess: func(AccessPool, []byte) (AccessInitializer, error) {
			return &testAccessInitializer{initialized: true}, nil
		},
	})
	fixture.request = admincli.FirstSourceAdmissionRequest{Apply: true, Intent: credential.FirstSourceAdmissionIntent{
		Version: 1, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000101", TargetID: credentialSetupInstance,
		ProjectID: "finance", Environment: "prod", CustomerOwnerID: "customer:one", OperatorPrincipalID: operator.Principal.ID,
		ConnectionID: "warehouse", BindingID: "binding:first", ExpectedPolicyRevision: policy.Revision, ExpectedPolicyDigest: policy.Digest,
		Endpoint:            credential.FirstSourceEndpoint{Host: "postgres.internal", Port: 5432, Database: "analytics", TLSMode: "require"},
		CredentialReference: credential.FirstSourceCredentialReference{ProjectID: "finance", Environment: "prod", SecretPath: "/customer/warehouse", SecretKey: "password"},
	}}
	return fixture
}

func (f *firstSourceFixture) counts(t *testing.T) (int, int, int64) {
	t.Helper()
	var admission, binding int
	var revision int64
	if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM credential.first_source_admission), (SELECT count(*) FROM connection_binding.target_connection_binding), (SELECT revision FROM access.authorization_policy)`).Scan(&admission, &binding, &revision); err != nil {
		t.Fatal(err)
	}
	return admission, binding, revision
}

func TestFirstSourceAdmissionAtomicPreviewReplayAndIntentConflict(t *testing.T) {
	f := newFirstSourceFixture(t)
	request := f.request
	request.Apply = false
	if err := f.ops.AdmitFirstSource(t.Context(), request, io.Discard); err != nil {
		t.Fatal(err)
	}
	if a, b, r := f.counts(t); a != 0 || b != 0 || r != f.policy.Revision {
		t.Fatalf("preview committed a=%d b=%d r=%d", a, b, r)
	}
	var first, replay bytes.Buffer
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, &first); err != nil {
		t.Fatal(err)
	}
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, &replay); err != nil {
		t.Fatal(err)
	}
	if first.String() != replay.String() {
		t.Fatal("exact replay changed public receipt")
	}
	if a, b, r := f.counts(t); a != 1 || b != 1 || r != f.policy.Revision+1 {
		t.Fatalf("invalid state a=%d b=%d r=%d", a, b, r)
	}
	var auditCount int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='credential.first_source.admitted'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d error=%v", auditCount, err)
	}
	policy, err := f.repo.AuthorizationPolicy(t.Context(), f.policy.Scope)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.request.Intent.Grant()
	if err != nil || len(policy.Grants) != 1 || !reflect.DeepEqual(policy.Grants[0], grant) {
		t.Fatalf("exact operator grant=%v error=%v", policy.Grants, err)
	}
	for _, mutation := range []func(*credential.FirstSourceAdmissionIntent){
		func(i *credential.FirstSourceAdmissionIntent) { i.Endpoint.Database = "other" },
		func(i *credential.FirstSourceAdmissionIntent) { i.BindingID = "binding:other" },
		func(i *credential.FirstSourceAdmissionIntent) { i.CredentialReference.SecretPath = "/customer/other" },
		func(i *credential.FirstSourceAdmissionIntent) { i.OperationID = "0198f2c0-7c7a-7f00-8a11-000000000102" },
	} {
		changed := f.request
		mutation(&changed.Intent)
		if err := f.ops.AdmitFirstSource(t.Context(), changed, io.Discard); err == nil {
			t.Fatal("changed operation intent accepted")
		}
	}
	for _, forbidden := range []string{"postgres.internal", "/customer/warehouse", "key-1"} {
		if strings.Contains(first.String(), forbidden) {
			t.Fatal("private configuration entered public receipt")
		}
	}
}

func TestFirstSourceAdmissionRemainsImmutableAfterAuditRetention(t *testing.T) {
	f := newFirstSourceFixture(t)
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); err != nil {
		t.Fatal(err)
	}
	journal, err := credentialpostgres.NewFirstSourceAdmissions(f.db, firstSourceAudit{repository: accesspostgres.New()})
	if err != nil {
		t.Fatal(err)
	}
	before, err := journal.AdmissionForTarget(t.Context(), f.request.Intent.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Admission(t.Context(), f.request.Intent.TargetID, "0198f2c0-7c7a-7f00-8a11-000000000102"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("foreign operation read: %v", err)
	}
	for _, command := range []string{`UPDATE credential.first_source_admission SET policy_revision = policy_revision + 1`, `DELETE FROM credential.first_source_admission`} {
		if _, err := f.db.Exec(t.Context(), command); err == nil {
			t.Fatal("immutable journal mutation succeeded")
		}
	}
	result, err := accesspostgres.NewMaintenance(f.maintenance).Prune(t.Context(), accesspostgres.RetentionStandard, time.Now().UTC().Add(time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedCount < 1 {
		t.Fatal("actual audit retention removed no events")
	}
	var remaining int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, f.request.Intent.OperationID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("admission audit remaining=%d error=%v", remaining, err)
	}
	after, err := journal.AdmissionForTarget(t.Context(), f.request.Intent.TargetID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("retention changed durable admission: %v", err)
	}
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); err != nil {
		t.Fatalf("exact replay after retention: %v", err)
	}
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if stored, err := journal.AdmissionForTargetTx(t.Context(), tx, f.request.Intent.TargetID); err != nil || !reflect.DeepEqual(stored, before) {
		t.Fatalf("transactional exact read: %v", err)
	}
}

func TestFirstSourceAdmissionAuditFailureRollsBackEveryMutation(t *testing.T) {
	f := newFirstSourceFixture(t)
	if _, err := f.db.Exec(t.Context(), `ALTER TABLE audit.audit_event ADD CONSTRAINT test_reject_first_source CHECK (action <> 'credential.first_source.admitted') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); err == nil {
		t.Fatal("audit failure accepted")
	}
	if a, b, r := f.counts(t); a != 0 || b != 0 || r != f.policy.Revision {
		t.Fatalf("audit failure committed a=%d b=%d r=%d", a, b, r)
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='connection.binding.created'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("binding audit survived rollback: %d %v", count, err)
	}
}

func TestFirstSourceAdmissionRefusesForeignScopeAndRevokedOperator(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*credential.FirstSourceAdmissionIntent)
	}{
		{"target", func(i *credential.FirstSourceAdmissionIntent) { i.TargetID = "target:foreign" }},
		{"project", func(i *credential.FirstSourceAdmissionIntent) {
			i.ProjectID = "foreign"
			i.CredentialReference.ProjectID = "foreign"
		}},
		{"owner", func(i *credential.FirstSourceAdmissionIntent) { i.CustomerOwnerID = "customer:foreign" }},
		{"policy", func(i *credential.FirstSourceAdmissionIntent) {
			i.ExpectedPolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFirstSourceFixture(t)
			request := f.request
			test.mutate(&request.Intent)
			if err := f.ops.AdmitFirstSource(t.Context(), request, io.Discard); err == nil {
				t.Fatal("foreign admission accepted")
			}
			if a, b, r := f.counts(t); a != 0 || b != 0 || r != f.policy.Revision {
				t.Fatal("foreign admission mutated authority")
			}
		})
	}
	f := newFirstSourceFixture(t)
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.DisablePrincipal(t.Context(), f.request.Intent.OperatorPrincipalID); err != nil {
		t.Fatal(err)
	}
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("revoked operator replay=%v", err)
	}
}

func TestFirstSourceAdmissionReplayRequiresCurrentExactGrant(t *testing.T) {
	f := newFirstSourceFixture(t)
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); err != nil {
		t.Fatal(err)
	}
	grant, err := f.request.Intent.Grant()
	if err != nil {
		t.Fatal(err)
	}
	grant.Permissions = grant.Permissions[1:]
	if _, err := f.repo.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: f.policy.Scope, Grant: grant, ExpectedRevision: f.policy.Revision + 1, IdempotencyKey: "revoke-manage"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ops.AdmitFirstSource(t.Context(), f.request, io.Discard); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("revoked grant replay=%v", err)
	}
}

func TestFirstSourceAdmissionRequiresStoppedInstanceBeforeDatabaseAccess(t *testing.T) {
	locked := errors.New("instance is running")
	intent := credential.FirstSourceAdmissionIntent{Version: 1, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000101", TargetID: credentialSetupInstance, ProjectID: "finance", Environment: "prod", CustomerOwnerID: "customer:one", OperatorPrincipalID: "principal:operator", ConnectionID: "warehouse", BindingID: "binding:first", ExpectedPolicyRevision: 1, ExpectedPolicyDigest: "sha256:" + strings.Repeat("a", 64), CredentialReference: credential.FirstSourceCredentialReference{ProjectID: projectgraph.ResourceID("finance"), Environment: "prod", SecretPath: "/customer/warehouse", SecretKey: "password"}}
	cfg := productionAdminConfig(t.TempDir())
	cfg.CredentialKeyringFile = "/private/keyring.json"
	ops := New(Dependencies{LoadConfig: func() (config.Config, error) { return cfg, nil }, AcquireLock: func(string) (adminoffline.Lock, error) { return nil, locked }, OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
		t.Fatal("database opened before lock")
		return nil, nil
	}})
	if err := ops.AdmitFirstSource(t.Context(), admincli.FirstSourceAdmissionRequest{Intent: intent, Apply: true}, io.Discard); !errors.Is(err, locked) {
		t.Fatalf("error=%v", err)
	}
}
