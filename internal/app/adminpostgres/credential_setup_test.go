package adminpostgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const credentialSetupInstance = "lvinst_0123456789abcdefghijklmnopqrstuv"

func TestCredentialSetupRequiresLockBeforeDatabaseAccess(t *testing.T) {
	cfg := productionAdminConfig(t.TempDir())
	cfg.CredentialKeyringFile = "/private/keyring.json"
	locked := errors.New("instance is running")
	operations := New(Dependencies{
		LoadConfig:  func() (config.Config, error) { return cfg, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) { return nil, locked },
		OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
			t.Fatal("opened database before lock")
			return nil, nil
		},
	})
	if err := operations.SetupCredentials(t.Context(), admincli.CredentialSetupRequest{OwnerID: "customer:one"}, io.Discard); !errors.Is(err, locked) {
		t.Fatalf("error=%v", err)
	}
}

func customerSetupDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "customer_setup")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := platformbootstrap.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := platformbootstrap.New(tx).EnsureInstanceID(t.Context(), credentialSetupInstance); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCustomerOwnerAuditFailureRollsBackAndReplayDoesNotDuplicate(t *testing.T) {
	db := customerSetupDB(t)
	failed := errors.New("audit unavailable")
	err := declareCustomerOwner(t.Context(), db, credentialSetupInstance, "customer:one", func(context.Context, pgx.Tx, access.AuditIntent) error { return failed })
	if !errors.Is(err, failed) {
		t.Fatalf("error=%v", err)
	}
	if _, err := platformbootstrap.New(db).CustomerOwner(t.Context()); !errors.Is(err, platformbootstrap.ErrNotFound) {
		t.Fatalf("owner survived audit rollback: %v", err)
	}
	audit := accesspostgres.New()
	appendAudit := func(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
		_, err := audit.RecordAuditEvent(ctx, tx, intent)
		return err
	}
	for range 2 {
		if err := declareCustomerOwner(t.Context(), db, credentialSetupInstance, "customer:one", appendAudit); err != nil {
			t.Fatal(err)
		}
	}
	if err := declareCustomerOwner(t.Context(), db, credentialSetupInstance, "customer:two", appendAudit); !errors.Is(err, platformbootstrap.ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	var count int
	var metadata string
	if err := db.QueryRow(t.Context(), `SELECT count(*), min(metadata::text) FROM audit.audit_event WHERE action = 'credential.owner.declared'`).Scan(&count, &metadata); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(metadata, "customer:one") || !strings.Contains(metadata, "offline_operator") {
		t.Fatalf("audit count=%d metadata=%s", count, metadata)
	}
}

// Wrap the real database with the adapter's small pool interface; setup owns
// closing its handle while the fixture retains the inspection connection.
type customerSetupPool struct{ *pgxpool.Pool }

func (p customerSetupPool) SQLDB() (*sql.DB, error) { return nil, errors.New("not used by fixture") }
func (p customerSetupPool) Close()                  {}

func TestCredentialSetupChecksInitializationAndKeyringBeforeDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initialized bool
		keyInstance string
		wantError   bool
	}{
		{"uninitialized", false, credentialSetupInstance, true},
		{"wrong keyring", true, "lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"configured", true, credentialSetupInstance, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := customerSetupDB(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := productionAdminConfig(root)
			cfg.CredentialKeyringFile = filepath.Join(cfg.HomeDir, "keyring.json")
			key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
			body := fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":"key-1","keys":[{"key_id":"key-1","key_base64":%q,"state":"active_write"}]}`, tc.keyInstance, key)
			if err := os.WriteFile(cfg.CredentialKeyringFile, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			operations := New(Dependencies{
				LoadConfig:  func() (config.Config, error) { return cfg, nil },
				AcquireLock: func(string) (adminoffline.Lock, error) { return &testAdminLock{}, nil },
				OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
					return customerSetupPool{Pool: db}, nil
				},
				VerifyBaseline: func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
				NewAccess: func(AccessPool, []byte) (AccessInitializer, error) {
					return &testAccessInitializer{initialized: tc.initialized}, nil
				},
			})
			var out bytes.Buffer
			err = operations.SetupCredentials(t.Context(), admincli.CredentialSetupRequest{OwnerID: "customer:one"}, &out)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			owner, readErr := platformbootstrap.New(db).CustomerOwner(t.Context())
			if tc.wantError && (!errors.Is(readErr, platformbootstrap.ErrNotFound) || out.Len() != 0) {
				t.Fatal("failed setup persisted ownership or returned success")
			}
			if !tc.wantError && (readErr != nil || owner != "customer:one" || !strings.Contains(out.String(), credentialSetupInstance)) {
				t.Fatalf("owner=%s readErr=%v output=%s", owner, readErr, out.String())
			}
			if strings.Contains(out.String(), key) {
				t.Fatal("output exposes key")
			}
		})
	}
}
