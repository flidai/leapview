package ducklake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

type upgradeSessionAdmission struct{}

func (upgradeSessionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	return extension.AdmittedExtension{Name: name, Path: "/opt/leapview/extensions/" + name + ".duckdb_extension", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}, nil
}

func validUpgradeSessionConfig(t *testing.T) PostgresCatalogUpgradeSessionConfig {
	t.Helper()
	return PostgresCatalogUpgradeSessionConfig{
		DataPath:           "s3://Bucket/lake/",
		TempDir:            t.TempDir(),
		MemoryMaxBytes:     256 << 20,
		TempMaxBytes:       512 << 20,
		MaxThreads:         2,
		ExtensionAdmission: upgradeSessionAdmission{},
		CredentialBootstrap: func(context.Context, driver.ExecerContext) error {
			return nil
		},
	}
}

func TestPostgresCatalogUpgradeSessionValidateRequiresBoundedPolicyAndOpaqueBootstrap(t *testing.T) {
	base := PostgresCatalogUpgradeSessionConfig{}
	if err := base.Validate(); !errors.Is(err, ErrPostgresCatalogUpgradeSession) {
		t.Fatalf("zero config error = %v", err)
	}
	valid := validUpgradeSessionConfig(t)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	withoutBootstrap := valid
	withoutBootstrap.CredentialBootstrap = nil
	if err := withoutBootstrap.Validate(); !errors.Is(err, ErrPostgresCatalogUpgradeSession) {
		t.Fatalf("missing credential bootstrap error = %v", err)
	}
	withoutAdmission := valid
	withoutAdmission.ExtensionAdmission = nil
	if err := withoutAdmission.Validate(); !errors.Is(err, ErrPostgresCatalogUpgradeSession) {
		t.Fatalf("missing extension admission error = %v", err)
	}
}

func TestPostgresCatalogUpgradeSessionStatementsAreBoundedAndDoNotAttach(t *testing.T) {
	config := validUpgradeSessionConfig(t)
	joined := strings.Join(upgradeResourceStatements(config), "\n")
	for _, required := range []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET memory_limit = '268435456B'",
		"SET max_temp_directory_size = '536870912B'",
		"SET threads = 2",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("resource statements missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "ATTACH") || strings.Contains(joined, "AUTOMATIC_MIGRATION") {
		t.Fatalf("resource statements contain catalog attachment or migration: %s", joined)
	}
	allowed, err := upgradeAllowedDirectories(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(allowed, "SET allowed_directories = [") || !strings.Contains(allowed, "s3://bucket/lake") || !strings.Contains(allowed, config.TempDir) {
		t.Fatalf("allowed-directories policy = %q", allowed)
	}
}

func TestOpenPostgresCatalogUpgradeSessionUsesInitializerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admission := cancelingUpgradeSessionAdmission{cancel: cancel}
	config := validUpgradeSessionConfig(t)
	config.ExtensionAdmission = admission
	_, err := OpenPostgresCatalogUpgradeSession(ctx, config)
	if !errors.Is(err, ErrPostgresCatalogUpgradeSession) || !errors.Is(err, context.Canceled) {
		t.Fatalf("initializer cancellation error = %v", err)
	}
}

func TestPostgresCatalogUpgradeSessionCloseIsIdempotent(t *testing.T) {
	var closes atomic.Int32
	closeErr := errors.New("close failed")
	connector := &upgradeSessionTestConnector{closes: &closes, err: closeErr}
	db := sql.OpenDB(connector)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session := &PostgresCatalogUpgradeSession{db: db, conn: conn}
	if err := session.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close error = %v", err)
	}
	if err := session.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("second close error = %v", err)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("underlying DuckDB connection closes = %d, want 1", got)
	}
}

type cancelingUpgradeSessionAdmission struct{ cancel context.CancelFunc }

func (a cancelingUpgradeSessionAdmission) AdmitExtension(ctx context.Context, _ string) (extension.AdmittedExtension, error) {
	a.cancel()
	return extension.AdmittedExtension{}, ctx.Err()
}

type upgradeSessionTestConnector struct {
	closes *atomic.Int32
	err    error
}

func (c *upgradeSessionTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &upgradeSessionTestConn{closes: c.closes, err: c.err}, nil
}
func (*upgradeSessionTestConnector) Driver() driver.Driver { return upgradeSessionTestDriver{} }

type upgradeSessionTestDriver struct{}

func (upgradeSessionTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("upgrade session test driver uses connector")
}

type upgradeSessionTestConn struct {
	closes *atomic.Int32
	err    error
}

func (c *upgradeSessionTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not used")
}
func (c *upgradeSessionTestConn) Close() error            { c.closes.Add(1); return c.err }
func (*upgradeSessionTestConn) Begin() (driver.Tx, error) { return nil, errors.New("not used") }
