//go:build linux && duckdb_arrow

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
)

const restoredJourneyProject = projectgraph.ResourceID("project:restored-authority")

// One real production application publishes and queries native DuckLake data,
// stops all application writers, and captures its home alongside one physical
// PostgreSQL backup containing BOTH control and DuckLake authorities. A second
// BuildProduction consumes only that restored state. No runtime, materializer,
// authorization, refresh executor, delivery coordinator, or claims are stubbed.
// This is disposable local-storage composition proof, not provider/PITR/S3 or
// browser-renderer qualification.
func TestPostgresPhysicalRestorePreservesApplicationJourneys(t *testing.T) {
	source := writeRestoredJourneySource(t, 1)
	if _, err := (devloop.FilesystemBuilder{SourceRoot: source, ProjectID: restoredJourneyProject}).Build(t.Context()); err != nil {
		t.Fatalf("compile restored journey source fixture: %v", err)
	}
	h := postgrestest.StartTLS(t) // Standalone; never clone the shared package server.
	roles := provisionPostgresOnboardingRoles(t, h)
	control := h.NewDatabase(t, "leapview_control")
	catalog := h.NewDatabase(t, ducklakepostgres.DefaultDuckLakeDatabase)
	grantPostgresOnboardingDatabases(t, h, control, catalog, roles)
	extensions := extensionfixture.New(t, "ducklake", "postgres")
	cfg := postgresOnboardingConfig(t, h, control, catalog, roles, extensions)
	cleanupRestoredJourneyDirectory(t, filepath.Dir(cfg.HomeDir))
	cfg.LocalAuth, cfg.APITokenOnlyAuth = true, false
	operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return cfg, nil }})
	var output bytes.Buffer
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &output); err != nil {
		t.Fatal(err)
	}
	initial, err := adminoffline.DecodeInitialCredentials(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.AcknowledgeInitialCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Reuse the actual onboarding pool admission, rather than fabricate an
	// admitted compatibility tuple in SQL.
	tuple := restoredJourneyRuntimeCompatibility(t, extensions)
	evidence, err := ducklake.RunLocalPoolConformance(t.Context(), filepath.Join(cfg.HomeDir, "conformance"), tuple, extensions.Admission)
	if err != nil {
		t.Fatal(err)
	}
	identity := physicalpool.PoolIdentity{
		StorageLocation: filepath.Join(cfg.HomeDir, "physical-pools"), StorageNamespace: "delivery", Region: "local", Tenant: "production",
		EncryptionDomain: "production", IsolationBoundary: "production", RetentionAuthority: "production",
		RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60}, Compatibility: tuple,
	}
	if err := operations.BootstrapPhysicalPool(t.Context(), adminoffline.PhysicalPoolBootstrapRequest{Pool: identity, Evidence: evidence, Apply: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeliveryPhysicalPoolID = pool.ID.String()
	cfg.DeliveryPhysicalPoolCompatibilityDigest, err = tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}

	// Provision real identities/policy as fixture input. The exercised routes
	// still resolve credentials and current policy through PostgreSQL.
	bootstrap, err := openPostgresControlPlane(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bootstrap.Stop(context.Background()) })
	instanceID, err := postgresauthority.ResolveInstanceIdentity(t.Context(), bootstrap.RuntimePool(), cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := postgresauthority.NewPostgresAuthorityGraph(bootstrap.RuntimePool(), bootstrap.MaintenancePool(), postgresauthority.PostgresAuthorityGraphOptions{TargetID: instanceID, FingerprintKey: []byte(cfg.TokenHashKey)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := graph.Access.CredentialForAPIToken(t.Context(), initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Access.ChangeLocalPassword(t.Context(), claim.Principal.ID, initial.TemporaryPassword, "restored-authority-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.DeploymentRepository.ClaimProject(t.Context(), deployment.ProjectClaimInput{ProjectID: restoredJourneyProject, Environment: "prod", ClaimedBy: claim.Principal.ID, ClaimedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := projectmodule.EnsureIdentity(t.Context(), graph.Project, restoredJourneyProject); err != nil {
		t.Fatal(err)
	}
	reviewer, err := graph.Access.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "restore-reviewer@example.test", DisplayName: "Independent restore reviewer", Password: "independent-reviewer-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := graph.Access.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "restore-outsider@example.test", DisplayName: "Unprivileged restore control", Password: "unprivileged-control-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: restoredJourneyProject.String(), Environment: "prod"}
	var policy access.AuthorizationPolicy
	var ownerPermissions, reviewerPermissions []access.PermissionPair
	for index, role := range []access.PermissionRole{access.PermissionRoleProjectAdmin, access.PermissionRoleEditor, access.PermissionRoleExplorer, access.PermissionRoleReleaseOperator, access.PermissionRoleReleaseApprover} {
		principalID := claim.Principal.ID
		if role == access.PermissionRoleReleaseApprover {
			principalID = reviewer.Principal.ID
		}
		binding, err := access.NewTypedRoleBinding(fmt.Sprintf("restore-role-%d", index), "Restored journey authority", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, role, restoredJourneyProject)
		if err != nil {
			t.Fatal(err)
		}
		policy, err = graph.Access.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, ExpectedRevision: policy.Revision, IdempotencyKey: binding.ID})
		if err != nil {
			t.Fatal(err)
		}
		if principalID == reviewer.Principal.ID {
			reviewerPermissions = append(reviewerPermissions, binding.Permissions...)
		} else {
			ownerPermissions = append(ownerPermissions, binding.Permissions...)
		}
	}
	// Preset editor/release roles deliberately exclude data upload and pipeline
	// execution. Grant these two exact resources explicitly, preserving the
	// production distinction between editing and running governed work.
	for index, input := range []struct {
		id     projectgraph.ResourceID
		kind   projectgraph.Kind
		action access.Action
	}{{"connection:restored", projectgraph.KindConnection, access.ActionConnectionUpload}, {"pipeline:restored-refresh", projectgraph.KindPipeline, access.ActionPipelineRun}} {
		resource, err := access.NewResourceRef(input.id, input.kind)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := access.NewExactPermissionPair(input.action, restoredJourneyProject, resource)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("restore-work-%d", index)
		policy, err = graph.Access.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: scope, Grant: access.AuthorizationGrant{ID: id, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: claim.Principal.ID}, Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}}, ExpectedRevision: policy.Revision, IdempotencyKey: id})
		if err != nil {
			t.Fatal(err)
		}
		ownerPermissions = append(ownerPermissions, pair)
	}
	issue := func(principalID, name string, permissions []access.PermissionPair) string {
		t.Helper()
		unique := make([]access.PermissionPair, 0, len(permissions))
		seen := make(map[string]bool, len(permissions))
		for _, permission := range permissions {
			if !seen[permission.Key()] {
				unique = append(unique, permission)
				seen[permission.Key()] = true
			}
		}
		token, _, err := graph.Access.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: principalID, Name: name, Permissions: unique, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	ownerToken := issue(claim.Principal.ID, "restore-owner", ownerPermissions)
	reviewerToken := issue(reviewer.Principal.ID, "restore-reviewer", reviewerPermissions)
	// Its token ceiling matches the owner, while its principal has no role.
	outsiderToken := issue(outsider.Principal.ID, "restore-outsider", ownerPermissions)
	session, err := graph.Access.CreateSession(t.Context(), claim.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ownerCredential, err := graph.Access.CredentialForAPIToken(t.Context(), ownerToken)
	if err != nil {
		t.Fatal(err)
	}
	sessionCredential, err := graph.Access.CredentialForSessionToken(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}

	application := startRestoredJourneyApplication(t, cfg)
	api := restoredJourneyAPI{t: t, application: application, owner: ownerToken, reviewer: reviewerToken, target: instanceID, control: bootstrap.RuntimePool()}
	uploadRestoredJourneyData(t, cfg, api, "order_id,revenue\no1,10\no2,20\no3,30\n", "before-backup")
	before := api.publish(source, "before-backup")
	initialQuery := api.assertQuery(ownerToken, "60", http.StatusOK)
	api.assertQuery(outsiderToken, "", http.StatusForbidden)
	api.assertSession(session, http.StatusOK)
	if err := application.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	// No application writer runs between this verified physical cluster point
	// and the home snapshot; no raw live PostgreSQL directory is copied.
	restoredHarness, restoredDatabases := h.RestorePhysical(t, control, catalog)
	homeBackup := filepath.Join(t.TempDir(), "snapshot")
	cleanupRestoredJourneyDirectory(t, homeBackup)
	homeDigest, err := hostinstall.CaptureStoppedDirectories(t.Context(), homeBackup, instanceID, map[string]string{"home": cfg.HomeDir})
	if err != nil {
		t.Fatal(err)
	}
	// The source server is stopped by RestorePhysical. A replacement
	// accidentally wired to the original endpoint cannot qualify the journey.
	if err := os.WriteFile(filepath.Join(cfg.HomeDir, "after-backup-only"), []byte("must not survive restore"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := hostinstall.RestoreStoppedDirectories(t.Context(), homeBackup, instanceID, homeDigest, map[string]string{"home": cfg.HomeDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.HomeDir, "after-backup-only")); !os.IsNotExist(err) {
		t.Fatal("restored home retained post-backup data")
	}
	restoredConfig := restoredJourneyConfig(cfg, restoredHarness, restoredDatabases[0], restoredDatabases[1], roles)
	restoredApplication := startRestoredJourneyApplication(t, restoredConfig)
	defer func() { _ = restoredApplication.Shutdown(context.Background()) }()
	api.application = restoredApplication
	api.assertSession(session, http.StatusOK)
	api.assertQuery(ownerToken, "60", http.StatusOK)
	api.assertQuery(outsiderToken, "", http.StatusForbidden)
	if got := api.request(http.MethodGet, "/api/v1/me", ownerToken, nil, http.StatusOK, nil); got["id"] != ownerCredential.Principal.ID {
		t.Fatalf("restored token principal drift: %v", got)
	}
	restoredBootstrap, err := openPostgresControlPlane(t.Context(), restoredConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoredBootstrap.Stop(context.Background()) })
	restoredGraph, err := postgresauthority.NewPostgresAuthorityGraph(restoredBootstrap.RuntimePool(), restoredBootstrap.MaintenancePool(), postgresauthority.PostgresAuthorityGraphOptions{TargetID: instanceID, FingerprintKey: []byte(cfg.TokenHashKey)})
	if err != nil {
		t.Fatal(err)
	}
	api.control = restoredBootstrap.RuntimePool()
	restoredCredential, err := restoredGraph.Access.CredentialForSessionToken(t.Context(), session)
	if err != nil || restoredCredential.ID != sessionCredential.ID || restoredCredential.TokenFingerprint != sessionCredential.TokenFingerprint || restoredCredential.PrincipalID != sessionCredential.PrincipalID {
		t.Fatalf("physical restore changed exact durable session authority: %v", err)
	}
	restoredPrincipal, err := restoredGraph.Access.PrincipalForToken(t.Context(), session)
	if err != nil || restoredPrincipal.ID != claim.Principal.ID || restoredPrincipal.Email != claim.Principal.Email {
		t.Fatalf("physical restore changed browser-session principal: %v", err)
	}
	restoredOwner, err := restoredGraph.Access.CredentialForAPIToken(t.Context(), ownerToken)
	if err != nil || restoredOwner.Token.ID != ownerCredential.Token.ID || restoredOwner.Token.TokenFingerprint != ownerCredential.Token.TokenFingerprint || restoredOwner.Principal.ID != ownerCredential.Principal.ID {
		t.Fatalf("physical restore changed exact API credential authority: %v", err)
	}
	restoredPolicy, err := restoredGraph.Access.AuthorizationPolicy(t.Context(), scope)
	if err != nil || restoredPolicy.Revision != policy.Revision || restoredPolicy.Digest != policy.Digest {
		t.Fatalf("physical restore changed exact policy authority: %v", err)
	}
	retained := api.request(http.MethodGet, api.deliveryPath()+"/publications/"+before["id"].(string), ownerToken, nil, http.StatusOK, nil)
	if retained["generationId"] != before["generationId"] || retained["planDigest"] != before["planDigest"] || retained["resultTargetRevision"] != before["resultTargetRevision"] {
		t.Fatalf("physical restore changed serving publication: before=%v restored=%v", before, retained)
	}

	// New data can be staged through restored authority, but an existing
	// generation's exact managed-data pin must not silently adopt that upload.
	// Canonical refresh must finish and serve a fresh snapshot of the same pin,
	// rather than merely create a queued marker or return the old generation.
	uploadRestoredJourneyData(t, restoredConfig, api, "order_id,revenue\no1,20\no2,40\no3,60\n", "after-restore")
	refreshPath := "/api/v1/projects/" + restoredJourneyProject.String() + "/refresh-runs"
	api.request(http.MethodPost, refreshPath, outsiderToken, map[string]any{"pipelineId": "pipeline:restored-refresh"}, http.StatusForbidden, nil)
	run := api.request(http.MethodPost, refreshPath, ownerToken, map[string]any{"pipelineId": "pipeline:restored-refresh"}, http.StatusAccepted, nil)
	api.await(http.MethodGet, refreshPath+"/"+run["id"].(string), ownerToken, "status", "succeeded")
	refreshed := api.assertQuery(ownerToken, "60", http.StatusOK)
	freshness, _ := refreshed["freshness"].(map[string]any)
	initialFreshness, _ := initialQuery["freshness"].(map[string]any)
	snapshotID := func(evidence map[string]any) uint64 {
		t.Helper()
		value, ok := evidence["snapshotId"].(string)
		id, err := strconv.ParseUint(value, 10, 64)
		if !ok || err != nil || id == 0 {
			t.Fatalf("missing native query snapshot evidence: %v", evidence)
		}
		return id
	}
	if snapshotID(freshness) <= snapshotID(initialFreshness) || refreshed["servingSnapshot"] == before["generationId"] || freshness["source"] != "refresh" || freshness["status"] != "current" {
		t.Fatalf("successful refresh did not serve a new pinned native generation: %v", refreshed)
	}

	// Recompile altered authored SQL and execute plan/build/seal, independent
	// reviewer approval, publication, and activation on restored repositories.
	writeRestoredJourneyModel(t, source, 2)
	after := api.publish(source, "after-restore")
	if after["generationId"] == before["generationId"] || after["id"] == before["id"] || after["resultTargetRevision"].(float64) <= before["resultTargetRevision"].(float64) {
		t.Fatalf("restored target did not publish a fresh fenced generation: before=%v after=%v", before, after)
	}
	api.assertQuery(ownerToken, "120", http.StatusOK)
	api.assertQuery(outsiderToken, "", http.StatusForbidden)
	assertRestoredJourneyQueryParity(t, api, restoredConfig, restoredGraph.Access, restoredBootstrap.RuntimePool(), session, ownerCredential.Principal.ID, outsider.Principal.ID, outsiderToken)
	api.assertSession(session, http.StatusFound)
	t.Log("verified one physical control/catalog backup + home restore preserves credentials, sessions, policy, publication and actual governed query; fresh canonical refresh and independently approved deployment change actual results with principal isolation")
}

func startRestoredJourneyApplication(t *testing.T, cfg config.Config) *Application {
	t.Helper()
	application, err := BuildProduction(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build restored-authority production application: %v", err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	if err := application.Start(t.Context()); err != nil {
		t.Fatalf("start restored-authority production application: %v", err)
	}
	return application
}

// Runtime views deliberately contain read-only directories. Make only these
// test-owned trees writable after all application/backup assertions finish so
// testing.TempDir can remove them without changing the exercised permissions.
func cleanupRestoredJourneyDirectory(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return nil
			}
			return os.Chmod(path, 0700)
		})
	})
}

func restoredJourneyRuntimeCompatibility(t *testing.T, extensions extensionfixture.Fixture) physicalpool.Compatibility {
	t.Helper()
	root := t.TempDir()
	environment, err := ducklake.Open(t.Context(), ducklake.Config{RootDir: root, CatalogPath: filepath.Join(root, "catalog.duckdb"), DataPath: filepath.Join(root, "objects"), MaxThreads: 2, MemoryMaxBytes: 256 << 20, TempMaxBytes: 512 << 20, ExtensionAdmission: extensions.Admission})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	rows, err := environment.Query(t.Context(), semanticquery.Plan{
		SQL:     "SELECT version() AS runtime, extension_version AS extension, (SELECT CAST(value AS VARCHAR) FROM lake.options() WHERE lower(option_name)='version' AND upper(scope)='GLOBAL' LIMIT 1) AS catalog FROM lake.settings() LIMIT 1",
		Columns: []string{"runtime", "extension", "catalog"},
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("discover actual admitted runtime tuple: %v: %v", rows, err)
	}
	component := func(key string) string { return strings.TrimPrefix(fmt.Sprint(rows[0][key]), "v") }
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:" + component("runtime"), DuckLakeExtension: "ducklake:" + component("extension"), CatalogFormat: "ducklake:" + component("catalog"), StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	if err := tuple.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual admitted fixture runtime: %+v", tuple)
	return tuple
}

func restoredJourneyConfig(cfg config.Config, h *postgrestest.Harness, control, catalog *postgrestest.Database, roles postgresOnboardingRoles) config.Config {
	tls := func(database *postgrestest.Database, role postgrestest.Role) string {
		return productionAdmissionTLSURL(database.PrivateURL(role), h.RootCertPath())
	}
	cfg.PostgresControlURL, cfg.PostgresControlMigratorURL = tls(control, roles.controlRuntime), tls(control, roles.controlMigrator)
	cfg.PostgresControlMaintenanceURL, cfg.PostgresControlReadonlyURL = tls(control, roles.controlMaintenance), tls(control, roles.controlReadonly)
	cfg.PostgresControlUpgradeCoordinatorURL = tls(control, roles.controlUpgrade)
	cfg.PostgresDuckLakeURL, cfg.PostgresDuckLakeMigratorURL = tls(catalog, roles.catalogRuntime), tls(catalog, roles.catalogMigrator)
	cfg.PostgresDuckLakeMaintenanceURL = tls(catalog, roles.catalogMaintenance)
	return cfg
}

type restoredJourneyAPI struct {
	t                       *testing.T
	application             *Application
	control                 *platformpostgres.Pool
	owner, reviewer, target string
}

func (a restoredJourneyAPI) request(method, path, token string, body any, status int, headers map[string]string) map[string]any {
	a.t.Helper()
	return a.requestContext(a.t.Context(), method, path, token, body, status, headers)
}

func (a restoredJourneyAPI) requestContext(ctx context.Context, method, path, token string, body any, status int, headers map[string]string) map[string]any {
	a.t.Helper()
	var input io.Reader
	if data, ok := body.([]byte); ok {
		input = bytes.NewReader(data)
	} else if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		input = bytes.NewReader(data)
	}
	req := httptest.NewRequestWithContext(ctx, method, "https://localhost"+path, input)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", journeyUUIDv7(a.t))
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res := httptest.NewRecorder()
	a.application.Handler().ServeHTTP(res, req)
	if res.Code != status {
		a.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.Code, status, res.Body)
	}
	var result map[string]any
	if status < 400 && res.Body.Len() != 0 {
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			a.t.Fatalf("decode %s: %v: %s", path, err, res.Body)
		}
	}
	return result
}

func (a restoredJourneyAPI) await(method, path, token, field, want string) map[string]any {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		result := a.requestContext(ctx, method, path, token, nil, http.StatusOK, nil)
		if result[field] == want {
			return result
		}
		if status, _ := result[field].(string); status == "failed" || status == "rejected" || status == "cancelled" || status == "indeterminate" {
			a.t.Fatalf("terminal %s: %v", path, result)
		}
		select {
		case <-ctx.Done():
			a.t.Fatalf("%s did not reach %s=%s: %v", path, field, want, result)
		case <-ticker.C:
		}
	}
}

func (a restoredJourneyAPI) deliveryPath() string {
	return "/api/v1/projects/" + restoredJourneyProject.String() + "/delivery"
}

func (a restoredJourneyAPI) publish(source, label string) map[string]any {
	a.t.Helper()
	snapshot, err := (devloop.FilesystemBuilder{SourceRoot: source, ProjectID: restoredJourneyProject, CandidateKey: label}).Build(a.t.Context())
	if err != nil {
		a.t.Fatal(err)
	}
	artifacts := make([]map[string]any, len(snapshot.Artifacts))
	for i, artifact := range snapshot.Artifacts {
		artifacts[i] = map[string]any{"path": artifact.Path, "digest": artifact.Digest, "sizeBytes": artifact.SizeBytes}
	}
	body := map[string]any{"artifactDigest": snapshot.Digest, "sourceOnly": true, "candidateKey": label, "artifacts": artifacts}
	syncPath := "/api/v1/projects/" + restoredJourneyProject.String() + "/candidate-sync"
	plan := a.request(http.MethodPost, syncPath+"/plan", a.owner, body, http.StatusOK, nil)
	missing := map[string]bool{}
	for _, digest := range plan["missingDigests"].([]any) {
		missing[digest.(string)] = true
	}
	for _, artifact := range snapshot.Artifacts {
		if !missing[artifact.Digest] {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(artifact.Digest, "sha256:"))
		if err != nil {
			a.t.Fatal(err)
		}
		a.request(http.MethodPut, syncPath+"/blobs/"+artifact.Digest, a.owner, artifact.Content, http.StatusCreated, map[string]string{"Content-Type": "application/octet-stream", "Content-Digest": "sha-256=:" + base64.StdEncoding.EncodeToString(raw) + ":", "Source-Synchronization-Plan": plan["planId"].(string)})
	}
	retained := a.request(http.MethodPost, syncPath+"/source", a.owner, body, http.StatusOK, map[string]string{"Source-Synchronization-Plan": plan["planId"].(string)})
	delivery := a.request(http.MethodPost, a.deliveryPath(), a.owner, map[string]any{"targetId": a.target, "operation": "code_change", "sourceDigest": retained["sourceDigest"], "sourceAttestationDigest": retained["sourceAttestationDigest"]}, http.StatusCreated, nil)
	build := a.request(http.MethodPost, a.deliveryPath()+"/plans/"+delivery["id"].(string)+"/build", a.owner, nil, http.StatusOK, nil)
	sealID, _ := build["snapshotSealId"].(string)
	if build["status"] != "sealed" || build["planDigest"] != delivery["planDigest"] || sealID == "" {
		a.t.Fatalf("build did not seal actual DuckLake snapshot: %v", build)
	}
	publication := a.request(http.MethodPost, a.deliveryPath()+"/candidates/"+build["candidateId"].(string)+"/publish", a.owner, nil, http.StatusAccepted, nil)
	approvalPath := a.deliveryPath() + "/publications/" + publication["id"].(string) + "/approval-requests"
	approval := a.request(http.MethodPost, approvalPath, a.owner, nil, http.StatusCreated, nil)
	a.request(http.MethodPost, approvalPath+"/"+approval["id"].(string)+"/approve", a.reviewer, map[string]any{"expectedRevision": approval["revision"]}, http.StatusOK, nil)
	committed := a.await(http.MethodGet, a.deliveryPath()+"/publications/"+publication["id"].(string), a.owner, "status", "committed")
	a.awaitActivation(publication["id"].(string))
	a.awaitRuntime()
	// Build reads intentionally require an active serving generation, unlike
	// bootstrap publication/approval operations. Read exact snapshot evidence
	// after initial activation, through the canonical generated read route.
	buildEvidence := a.request(http.MethodGet, a.deliveryPath()+"/builds/"+build["id"].(string), a.owner, nil, http.StatusOK, nil)
	snapshotID, _ := buildEvidence["ducklakeSnapshotId"].(float64)
	if buildEvidence["snapshotSealId"] != sealID || snapshotID < 1 {
		a.t.Fatalf("activated build lost exact native snapshot seal: %v", buildEvidence)
	}
	return committed
}

// A committed publication precedes runtime reconciliation. Wait for the exact
// durable activation operation, not the expected query result or generic health
// of the previous generation, before testing the newly deployed behavior.
func (a restoredJourneyAPI) awaitActivation(publicationID string) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		var status string
		err := a.control.QueryRow(ctx, `SELECT count(*), COALESCE(min(status::text), '') FROM jobs.job_history
WHERE kind = 'delivery.approval.activate' AND resource_kind = 'delivery_publication' AND resource_id = $1`, publicationID).Scan(&count, &status)
		if err != nil || count != 1 {
			a.t.Fatalf("exact publication activation authority: count=%d status=%s err=%v", count, status, err)
		}
		if status == "succeeded" {
			return
		}
		if status == "failed" || status == "cancelled" {
			a.t.Fatalf("publication activation terminal status=%s", status)
		}
		select {
		case <-ctx.Done():
			a.t.Fatalf("publication activation did not complete: status=%s", status)
		case <-ticker.C:
		}
	}
}

func (a restoredJourneyAPI) awaitRuntime() {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		res := httptest.NewRecorder()
		a.application.Handler().ServeHTTP(res, httptest.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/readyz", nil))
		var readiness struct {
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &readiness); err != nil || (res.Code != http.StatusOK && res.Code != http.StatusServiceUnavailable) {
			a.t.Fatalf("native readiness status=%d body=%s err=%v", res.Code, res.Body, err)
		}
		if res.Code == http.StatusOK && readiness.Checks["runtime"] == "ok" {
			return
		}
		select {
		case <-ctx.Done():
			a.t.Fatalf("committed publication never acquired an actual active runtime: status=%d body=%s", res.Code, res.Body)
		case <-ticker.C:
		}
	}
}

func (a restoredJourneyAPI) assertQuery(token, total string, status int) map[string]any {
	a.t.Helper()
	result := a.request(http.MethodPost, "/api/v1/semantic-models/semantic-model:restored/query", token, map[string]any{"metrics": []map[string]string{{"field": "revenue"}}, "limit": 10}, status, nil)
	if status != http.StatusOK {
		return result
	}
	rows, ok := result["rows"].([]any)
	if !ok || len(rows) != 1 {
		a.t.Fatalf("query rows: %v", result)
	}
	row, ok := rows[0].([]any)
	if !ok || len(row) != 1 || fmt.Sprint(row[0]) != total {
		a.t.Fatalf("governed query result=%v want=%s", result, total)
	}
	return result
}

func (a restoredJourneyAPI) assertSession(token string, status int) {
	a.t.Helper()
	req := httptest.NewRequestWithContext(a.t.Context(), http.MethodGet, "https://localhost/admin/profile", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-lv_session", Value: token})
	res := httptest.NewRecorder()
	a.application.Handler().ServeHTTP(res, req)
	if res.Code != status {
		a.t.Fatalf("restored browser session status=%d want=%d body=%s", res.Code, status, res.Body)
	}
}

func uploadRestoredJourneyData(t *testing.T, cfg config.Config, api restoredJourneyAPI, data, label string) {
	t.Helper()
	blobs, err := filesystem.New(filepath.Join(cfg.ManagedDataDir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(data))
	digest := hex.EncodeToString(hash[:])
	if _, err := blobs.Put(t.Context(), storage.Blob{SHA256: digest, Size: int64(len(data))}, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + restoredJourneyProject.String() + "/connections/connection:restored/upload-sessions"
	body := map[string]any{"manifest": map[string]any{"files": []map[string]any{{"path": "orders.csv", "size": len(data), "sha256": digest}}}}
	upload := api.request(http.MethodPost, path, api.owner, body, http.StatusCreated, map[string]string{"Idempotency-Key": "upload-" + label})
	api.request(http.MethodPost, path+"/"+upload["id"].(string)+"/finalize", api.owner, nil, http.StatusAccepted, nil)
	api.await(http.MethodGet, path+"/"+upload["id"].(string), api.owner, "status", "completed")
}

func writeRestoredJourneySource(t *testing.T, multiplier int) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"connections/restored.yaml":     "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata:\n  id: connection:restored\n  name: restored\nspec:\n  type: managed\n  defaults:\n    csv:\n      header: true\n",
		"sources/orders.yaml":           "apiVersion: leapview.dev/v1\nkind: Source\nmetadata:\n  id: source:restored.orders\n  name: restored.orders\nspec:\n  connection: restored\n  location:\n    type: path\n    path: orders.csv\n    format: csv\n  schema:\n    mode: strict\n  fields:\n    - {name: order_id, datatype: String}\n    - {name: revenue, datatype: Integer}\n",
		"semantic-models/restored.yaml": "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata:\n  id: semantic-model:restored\n  name: restored_metrics\nspec:\n  datasets:\n    - name: orders\n      model: orders\n      metrics:\n        - {name: revenue, type: simple, agg: sum, empty: zero}\n",
		"pipelines/restored.yaml":       "apiVersion: leapview.dev/v1\nkind: Pipeline\nmetadata:\n  id: pipeline:restored-refresh\n  name: restored-refresh\nspec:\n  selection:\n    semanticModel: restored_metrics\n",
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeRestoredJourneyModel(t, root, multiplier)
	return root
}

func writeRestoredJourneyModel(t *testing.T, root string, multiplier int) {
	t.Helper()
	path := filepath.Join(root, "models", "orders.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Model\nmetadata:\n  id: model:orders\n  name: orders\nspec:\n  definition:\n    type: sql\n    sql: 'SELECT order_id, CAST(revenue AS BIGINT) * %d AS revenue FROM source.\"restored.orders\"'\n  entities:\n    - {name: order, type: primary, fields: [order_id]}\n  grain:\n    entity: order\n  fields:\n    - {name: order_id, datatype: String}\n    - {name: revenue, datatype: Integer}\n", multiplier)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
