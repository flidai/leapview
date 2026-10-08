package credentialagent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type agentLifecycleFixture struct {
	adapter                      *AgentCredentials
	pool                         *pgxpool.Pool
	store                        *agentpostgres.Repository
	gate                         *credential.ProviderAdmission
	coordinator                  *credential.ActivationCoordinator
	config                       AgentCredentialConfig
	repository                   *credentialpostgres.Repository
	failAudit, failInstall, deny bool
	probes                       int
	installed                    agentmodule.ProviderConfig
}

func newAgentLifecycleFixture(t *testing.T) *agentLifecycleFixture {
	t.Helper()
	return newAgentLifecycleFixtureWithPoolLimit(t, 8)
}

func newAgentLifecycleFixtureWithPoolLimit(t *testing.T, limit int32) *agentLifecycleFixture {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "agent_credential_activation")
	poolConfig, err := pgxpool.ParseConfig(database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = limit
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = platformbootstrap.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = credentialpostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), agentpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	f := &agentLifecycleFixture{pool: pool, store: agentpostgres.NewRepository(pool)}
	audit := func(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
		if f.failAudit && intent.Action == "credential.activation.committed" {
			return fmt.Errorf("audit unavailable")
		}
		_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
		return err
	}
	repository, err := credentialpostgres.New(pool, credentialAuditAdapter{record: audit})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	const instance = "lvinst_0123456789abcdefghijklmnopqrstuv"
	body := fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":"key-1","keys":[{"key_id":"key-1","key_base64":%q,"state":"active_write"}]}`, instance, key)
	if err = os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	owner := platformbootstrap.New(pool)
	if err = owner.EnsureInstanceID(t.Context(), instance); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.DeclareCustomerOwner(t.Context(), "customer:one"); err != nil {
		t.Fatal(err)
	}
	f.config = AgentCredentialConfig{Pool: pool, Store: f.store, RecordAudit: audit, InstanceID: instance, KeyringPath: path, CustomerOwner: owner,
		CustomerOwnerTx: func(ctx context.Context, tx pgx.Tx) (string, error) { return owner.WithTx(tx).CustomerOwner(ctx) },
		Authorize: func(_ context.Context, actor string, pair access.PermissionPair) error {
			if f.deny || actor != "admin" || pair.Action != access.ActionPlatformSettingsUpdate || pair.Target.InstanceID != instance {
				return credential.ErrForbidden
			}
			return nil
		},
		LockFence: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(8193645)")
			return err
		},
		Probe: func(_ context.Context, c agentmodule.ProviderConfig) error {
			f.probes++
			if c.APIKey == "" {
				return fmt.Errorf("missing key")
			}
			return nil
		},
	}
	f.config.AuthorizeTx = func(ctx context.Context, _ pgx.Tx, actor string, pair access.PermissionPair) error {
		return f.config.Authorize(ctx, actor, pair)
	}
	f.repository = repository
	f.restart(t)
	return f
}
func (f *agentLifecycleFixture) restart(t *testing.T) {
	t.Helper()
	adapter, err := NewAgentCredentials(t.Context(), f.repository, f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = adapter
	f.gate = credential.NewProviderAdmission()
	install := func(ctx context.Context, revision int64) error {
		if f.failInstall {
			return fmt.Errorf("runtime unavailable")
		}
		record, err := f.store.CurrentConfiguration(ctx)
		if err != nil {
			return err
		}
		if record.Revision != revision {
			return credential.ErrConflict
		}
		return adapter.UseConfiguration(ctx, record, func(c agentmodule.ProviderConfig) error { f.installed = c; return nil })
	}
	if err = adapter.BindRuntime(install, func(ctx context.Context) error {
		record, err := f.store.CurrentConfiguration(ctx)
		if err == agentmodule.ErrConfigurationNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return install(ctx, record.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	f.coordinator, err = credential.NewActivationCoordinator(adapter, adapter, f.gate, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = adapter.SetCoordinator(f.coordinator); err != nil {
		t.Fatal(err)
	}
}
func agentCandidateInput(key string) agentmodule.ConfigurationInput {
	return agentmodule.ConfigurationInput{Enabled: true, Model: "fixture-model", BaseURL: "https://provider.example/v1", APIMode: "responses", APIKey: key}
}

func TestPostgreSQLAgentActivationDraftDoesNotActivateAndHistoryPinsExactVersion(t *testing.T) {
	f := newAgentLifecycleFixture(t)
	input := agentCandidateInput("private-key-A")
	token, err := f.adapter.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if f.probes != 1 || f.installed.APIKey != "" {
		t.Fatal("isolated validation changed runtime")
	}
	if _, err = f.store.CurrentConfiguration(t.Context()); err != agentmodule.ErrConfigurationNotFound {
		t.Fatalf("draft changed current: %v", err)
	}
	changed := input
	changed.BaseURL = "https://different.example/v1"
	if _, err = f.adapter.Activate(t.Context(), "admin", 0, changed, token); err == nil {
		t.Fatal("accepted changed destination")
	}
	if _, err = f.adapter.Activate(t.Context(), "other-admin", 0, input, token); err == nil {
		t.Fatal("accepted another actor")
	}
	first, err := f.adapter.Activate(t.Context(), "admin", 0, input, token)
	if err != nil {
		t.Fatal(err)
	}
	if !f.gate.Ready() || f.installed.APIKey != input.APIKey || first.CredentialVersionID == "" || len(first.Credential) != 0 || first.Config.APIKey != "" {
		t.Fatal("activation did not install exact non-plaintext reference")
	}
	input.APIKey = "private-key-B"
	input.BaseURL = "https://next.example/v1"
	token, err = f.adapter.Test(t.Context(), "admin", 1, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.adapter.Activate(t.Context(), "admin", 1, input, token)
	if err != nil {
		t.Fatal(err)
	}
	if second.CredentialVersionID == first.CredentialVersionID {
		t.Fatal("version reused")
	}
	if err = f.adapter.UseConfiguration(t.Context(), first, func(c agentmodule.ProviderConfig) error {
		if c.APIKey != "private-key-A" || c.BaseURL != "https://provider.example/v1" {
			t.Fatal("historical version substituted latest key/destination")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	forged := first
	forged.Config.BaseURL = second.Config.BaseURL
	if err = f.adapter.UseConfiguration(t.Context(), forged, func(agentmodule.ProviderConfig) error { t.Fatal("forged pin reached runtime"); return nil }); err == nil {
		t.Fatal("accepted forged history pin")
	}
	f.restart(t)
	if err = f.coordinator.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !f.gate.Ready() || f.installed.APIKey != "private-key-B" {
		t.Fatal("restart did not restore current exact version")
	}
	var metadata string
	if err = f.pool.QueryRow(t.Context(), "SELECT string_agg(metadata::text, '') FROM audit.audit_event").Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, "private-key-") {
		t.Fatal("audit leaked credential")
	}
}

func TestPostgreSQLAgentActivationAuditFailureRollsBackPointerAndRetryRestoresCommittedRuntime(t *testing.T) {
	f := newAgentLifecycleFixture(t)
	input := agentCandidateInput("private-key")
	token, err := f.adapter.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	f.failAudit = true
	if _, err = f.adapter.Activate(t.Context(), "admin", 0, input, token); err == nil {
		t.Fatal("ignored audit failure")
	}
	if _, err = f.store.CurrentConfiguration(t.Context()); err != agentmodule.ErrConfigurationNotFound {
		t.Fatalf("failed audit committed config: %v", err)
	}
	if f.gate.Ready() {
		t.Fatal("failed activation admitted work")
	}
	pending, err := f.adapter.repository.GetPendingActivationRequest(t.Context(), f.config.InstanceID)
	if err != nil || pending.State != "switching" {
		t.Fatalf("retry authority=%s err=%v", pending.State, err)
	}
	f.failAudit = false
	f.failInstall = true
	if _, err = f.adapter.Activate(t.Context(), "admin", 0, input, token); err == nil {
		t.Fatal("ignored runtime install failure")
	}
	pending, err = f.adapter.repository.GetPendingActivationRequest(t.Context(), f.config.InstanceID)
	if err != nil || pending.State != "committed" {
		t.Fatalf("commit authority=%s err=%v", pending.State, err)
	}
	saved, err := f.store.CurrentConfiguration(t.Context())
	if err != nil || saved.CredentialVersionID != pending.Request.VersionID {
		t.Fatalf("pointer and journal differ: %v", err)
	}
	if f.gate.Ready() {
		t.Fatal("committed-but-unready admitted work")
	}
	f.failInstall = false
	f.restart(t)
	if err = f.coordinator.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !f.gate.Ready() || f.installed.APIKey != input.APIKey {
		t.Fatal("restart failed exact committed recovery")
	}
	if _, err = f.coordinator.AbortActivation(t.Context(), "admin", f.adapter.resource(), pending.Request.OperationID); err == nil {
		t.Fatal("aborted committed activation")
	}
}

func TestPostgreSQLAgentActivationDisabledKeyRemovalUsesSameLifecycle(t *testing.T) {
	f := newAgentLifecycleFixture(t)
	input := agentCandidateInput("")
	input.Enabled = false
	input.RemoveKey = true
	token, err := f.adapter.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if f.probes != 0 {
		t.Fatal("disabled candidate contacted provider")
	}
	record, err := f.adapter.Activate(t.Context(), "admin", 0, input, token)
	if err != nil {
		t.Fatal(err)
	}
	if record.Enabled || record.CredentialVersionID == "" || f.installed.APIKey != "" || !f.gate.Ready() {
		t.Fatal("disabled configuration not durably activated")
	}
}

func TestPostgreSQLAgentActivationFreshProbeRetriesSamePendingVersion(t *testing.T) {
	f := newAgentLifecycleFixture(t)
	input := agentCandidateInput("private-key")
	token, err := f.adapter.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	f.failAudit = true
	if _, err = f.adapter.Activate(t.Context(), "admin", 0, input, token); err == nil {
		t.Fatal("expected failed commit")
	}
	original, err := f.adapter.repository.GetPendingActivationRequest(t.Context(), f.config.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.APIKey = "different-key"
	if _, err = f.adapter.Test(t.Context(), "admin", 0, changed); err == nil {
		t.Fatal("pending operation substituted a new key")
	}
	fresh, err := f.adapter.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == token || strings.Split(fresh, ".")[1] != original.Request.OperationID {
		t.Fatal("fresh validation replaced operation identity")
	}
	f.failAudit = false
	saved, err := f.adapter.Activate(t.Context(), "admin", 0, input, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CredentialVersionID != original.Request.VersionID {
		t.Fatal("retry substituted version")
	}
}

func TestPostgreSQLAgentActivationPrecommitAbortRestoresCurrentAndReleasesFence(t *testing.T) {
	f := newAgentLifecycleFixture(t)
	input := agentCandidateInput("private-key")
	token, err := f.adapter.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	f.failAudit = true
	if _, err = f.adapter.Activate(t.Context(), "admin", 0, input, token); err == nil {
		t.Fatal("expected failed commit")
	}
	f.failAudit = false
	if err = f.adapter.AbortConfiguration(t.Context(), "admin"); err != nil {
		t.Fatal(err)
	}
	if !f.gate.Ready() {
		t.Fatal("confirmed abort did not reopen admission")
	}
	if _, err = f.store.CurrentConfiguration(t.Context()); err != agentmodule.ErrConfigurationNotFound {
		t.Fatalf("abort changed pointer: %v", err)
	}
	input.APIKey = "replacement-key"
	if _, err = f.adapter.Test(t.Context(), "admin", 0, input); err != nil {
		t.Fatal(err)
	}
}

type credentialAuditAdapter struct {
	record func(context.Context, pgx.Tx, access.AuditIntent) error
}

func (a credentialAuditAdapter) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	return a.record(ctx, tx, intent)
}

func TestPostgreSQLAgentActivationUsesSingleConnectionThroughRestart(t *testing.T) {
	f := newAgentLifecycleFixtureWithPoolLimit(t, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input := agentCandidateInput("private-one-connection-key")
	token, err := f.adapter.Test(ctx, "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	f.failInstall = true
	if _, err = f.adapter.Activate(ctx, "admin", 0, input, token); err == nil {
		t.Fatal("accepted failed runtime installation")
	}
	// A database acquisition deadlock must not be mistaken for the injected
	// post-commit failure; restart must finish the exact committed operation.
	if err = ctx.Err(); err != nil {
		t.Fatalf("activation exhausted pool: %v", err)
	}
	saved, err := f.store.CurrentConfiguration(ctx)
	if err != nil || saved.CredentialVersionID == "" {
		t.Fatalf("missing committed configuration: %v", err)
	}
	f.failInstall = false
	f.restart(t)
	if err = f.coordinator.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.gate.Ready() || f.installed.APIKey != input.APIKey {
		t.Fatal("restart did not install committed credential")
	}
}
