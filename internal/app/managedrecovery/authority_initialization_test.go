package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagedAuthorityInitializationUsesCleanTLSDatabaseAndDedicatedRoles(t *testing.T) {
	h := postgrestest.StartTLS(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "normal-runtime-component", Login: true})
	bootstrapRole := "limited_initializer_" + digestBytes([]byte(t.Name()))[7:23]
	bootstrap := h.EnsureRole(t, postgrestest.Role{Name: bootstrapRole, Password: "private limited bootstrap", Login: true})
	database := h.NewDatabase(t, "")
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var systemID string
	if err := admin.QueryRow(t.Context(), "SELECT system_identifier::text FROM pg_control_system()").Scan(&systemID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	owner := "leapview_recovery_owner_" + digestBytes([]byte(database.Name))[7:23]
	operatorRole := "leapview_recovery_operator_" + digestBytes([]byte(database.Name))[7:23]
	operator := postgrestest.Role{Name: operatorRole, Password: "private initializer 'quoted password", Login: true}
	adminURL, err := url.Parse(database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	adminURL.RawQuery = "sslmode=verify-full"
	operatorURL, err := url.Parse(database.PrivateURL(operator))
	if err != nil {
		t.Fatal(err)
	}
	operatorURL.RawQuery = "sslmode=verify-full"
	adminURL.Host = operatorURL.Host
	ca, err := os.ReadFile(h.RootCertPath())
	if err != nil {
		t.Fatal(err)
	}
	input := ManagedAuthorityInitializationInput{SchemaVersion: 1, OwnerRole: owner, OriginalSystemIdentifier: "1", ReceiptFile: filepath.Join(root, "receipt.json"), Bootstrap: AuthorityInput{URLFile: filepath.Join(root, "admin-url"), RootCAFile: filepath.Join(root, "ca"), Role: adminURL.User.Username(), SystemIdentifier: systemID}, Operator: AuthorityInput{URLFile: filepath.Join(root, "operator-url"), RootCAFile: filepath.Join(root, "ca"), Role: operatorRole, SystemIdentifier: systemID}}
	for path, value := range map[string][]byte{input.Bootstrap.URLFile: []byte(adminURL.String()), input.Operator.URLFile: []byte(operatorURL.String()), input.Bootstrap.RootCAFile: ca} {
		if err := os.WriteFile(path, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	document, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "input.json")
	if err := os.WriteFile(inputPath, document, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadManagedAuthorityInitializationInput(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	foreignURL := *operatorURL
	foreignURL.Path = "/foreign_database"
	if err := os.WriteFile(input.Operator.URLFile, []byte(foreignURL.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Initialize(t.Context()); err == nil {
		t.Fatal("foreign operator endpoint accepted")
	}
	if err := os.WriteFile(input.Operator.URLFile, []byte(operatorURL.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inputPath, input.ReceiptFile); err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Initialize(t.Context()); err == nil {
		t.Fatal("linked receipt accepted")
	}
	if err := os.Remove(input.ReceiptFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input.ReceiptFile, []byte(`{"schemaVersion":1,"database":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Initialize(t.Context()); err == nil {
		t.Fatal("foreign receipt accepted before initialization")
	}
	var createdRoles int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_roles WHERE rolname IN ($1,$2)", owner, operatorRole).Scan(&createdRoles); err != nil || createdRoles != 0 {
		t.Fatal("invalid initialization input caused role effects")
	}
	if err := os.Remove(input.ReceiptFile); err != nil {
		t.Fatal(err)
	}
	receipt, err := parsed.Initialize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "initialized" || receipt.SystemIdentifier != systemID {
		t.Fatal("incomplete exact initialization receipt")
	}
	replay, err := parsed.Initialize(t.Context())
	if err != nil || replay != receipt {
		t.Fatalf("exact initialization replay: %v", err)
	}
	stat, err := os.Stat(input.ReceiptFile)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("authority receipt not private")
	}
	pool, err := OpenManagedAuthority(t.Context(), input.Operator, []providerrestore.PrimaryEnrollment{{SystemIdentifier: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ledger := refreshpostgres.NewRecoveryLedger(pool)
	occurrence, created, err := ledger.Enqueue(t.Context(), recovery.EnqueueInput{ScheduleID: "exact-init", Scenario: "managed-local-restore", Operation: recovery.OperationRestore, PolicyVersion: "init-test", PolicySHA256: strings.Repeat("a", 64), TargetScope: "target", ArtifactIdentity: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64), PlannedAt: now, StaleAfter: time.Hour}, now)
	if err != nil || !created {
		t.Fatalf("operator actual enqueue: %v", err)
	}
	claimed, ok, err := ledger.ClaimExact(t.Context(), occurrence.ID, recovery.ClaimInput{WorkerID: "controller", Actor: "operator", Now: now, Lease: time.Minute})
	if err != nil || !ok {
		t.Fatalf("operator exact claim: %v", err)
	}
	if err := ledger.Start(t.Context(), claimed.ID, claimed.Fence, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Heartbeat(t.Context(), claimed.ID, claimed.Fence, now.Add(2*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{"public DDL": "CREATE TABLE public.forbidden(id int)", "authority DDL": "CREATE TABLE recovery.forbidden(id int)", "schema owner": "SET ROLE " + pgx.Identifier{owner}.Sanitize(), "marker mutation": "UPDATE managed_recovery_authority.identity SET status='forged'", "ledger deletion": "DELETE FROM refresh.recovery_qualification_occurrence", "job authority": "SELECT * FROM jobs.job_history"} {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), sql); err == nil {
				t.Fatal("operator acquired forbidden authority")
			} else {
				var pgError *pgconn.PgError
				if !errors.As(err, &pgError) || pgError.Code != "42501" {
					t.Fatalf("unexpected denial: %v", err)
				}
			}
		})
	}
	runtimeConn, err := pgx.Connect(t.Context(), database.PrivateURL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeConn.Close(context.Background())
	for _, sql := range []string{"SELECT * FROM recovery.recovery_set", "SELECT * FROM refresh.recovery_qualification_occurrence", "SELECT * FROM jobs.job_history"} {
		if _, err := runtimeConn.Exec(t.Context(), sql); err == nil {
			t.Fatal("normal application runtime gained independent authority access")
		}
	}
	var ownerCanLogin, operatorSuper bool
	if err := admin.QueryRow(t.Context(), "SELECT rolcanlogin FROM pg_roles WHERE rolname=$1", owner).Scan(&ownerCanLogin); err != nil || ownerCanLogin {
		t.Fatal("schema owner can log in")
	}
	if err := admin.QueryRow(t.Context(), "SELECT rolsuper FROM pg_roles WHERE rolname=$1", operatorRole).Scan(&operatorSuper); err != nil || operatorSuper {
		t.Fatal("operator became superuser")
	}
	// Altered receipts and occupied databases must fail without consuming data.
	tampered := receipt
	tampered.Database = "foreign"
	value, _ := json.Marshal(tampered)
	if err := os.WriteFile(input.ReceiptFile, value, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Initialize(t.Context()); err == nil {
		t.Fatal("foreign receipt accepted")
	}
	occupied := h.NewDatabase(t, "")
	occupiedAdmin, err := pgx.Connect(t.Context(), occupied.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer occupiedAdmin.Close(context.Background())
	if _, err := occupiedAdmin.Exec(t.Context(), "CREATE TABLE public.customer_data(id int);INSERT INTO public.customer_data VALUES(42)"); err != nil {
		t.Fatal(err)
	}
	occupiedPool, err := pgxpool.New(t.Context(), occupied.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer occupiedPool.Close()
	request := ManagedAuthorityInitializationRequest{SystemIdentifier: systemID, Database: occupied.Name, OwnerRole: "leapview_recovery_owner_occupied_" + digestBytes([]byte(occupied.Name))[7:15], OperatorRole: "leapview_recovery_operator_occupied_" + digestBytes([]byte(occupied.Name))[7:15], OperatorPassword: "private"}
	if _, err := InitializeManagedAuthority(t.Context(), occupiedPool, request); err == nil {
		t.Fatal("occupied customer database was initialized")
	}
	var marker int
	if err := occupiedAdmin.QueryRow(t.Context(), "SELECT id FROM public.customer_data").Scan(&marker); err != nil || marker != 42 {
		t.Fatal("occupied database data changed")
	}
	// A bootstrap may create roles but lack permission to grant database CREATE.
	// Failure after role creation must roll back every role and schema effect.
	rollbackDB := h.NewDatabase(t, "")
	rollbackAdmin, err := pgx.Connect(t.Context(), rollbackDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackAdmin.Close(context.Background())
	for _, sql := range []string{"ALTER ROLE " + pgx.Identifier{bootstrapRole}.Sanitize() + " CREATEROLE", "REVOKE CREATE ON DATABASE " + pgx.Identifier{rollbackDB.Name}.Sanitize() + " FROM PUBLIC", "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO " + pgx.Identifier{bootstrapRole}.Sanitize()} {
		if _, err := rollbackAdmin.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	limitedPool, err := pgxpool.New(t.Context(), rollbackDB.PrivateURL(bootstrap))
	if err != nil {
		t.Fatal(err)
	}
	defer limitedPool.Close()
	rollbackRequest := ManagedAuthorityInitializationRequest{SystemIdentifier: systemID, Database: rollbackDB.Name, OwnerRole: "leapview_recovery_owner_rollback_" + digestBytes([]byte(rollbackDB.Name))[7:15], OperatorRole: "leapview_recovery_operator_rollback_" + digestBytes([]byte(rollbackDB.Name))[7:15], OperatorPassword: "private"}
	if _, err := InitializeManagedAuthority(t.Context(), limitedPool, rollbackRequest); err == nil {
		t.Fatal("bootstrap without database ownership initialized authority")
	}
	var remaining int
	if err := rollbackAdmin.QueryRow(t.Context(), "SELECT count(*) FROM pg_roles WHERE rolname IN ($1,$2)", rollbackRequest.OwnerRole, rollbackRequest.OperatorRole).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("failed initialization retained dedicated roles")
	}
	if err := rollbackAdmin.QueryRow(t.Context(), "SELECT count(*) FROM pg_namespace WHERE nspname IN ('jobs','refresh','recovery','managed_recovery_authority')").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("failed initialization retained authority schemas")
	}
}
