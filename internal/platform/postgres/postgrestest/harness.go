// Package postgrestest provides a bounded, real PostgreSQL 18 conformance
// environment for tests.  Each harness owns one disposable container and can
// create isolated databases and explicitly provisioned roles within it.
package postgrestest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	platformdb "github.com/flidai/leapview/internal/platform/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgreSQL18Image is the pinned image used by every PostgreSQL conformance
// lane.  Changing it requires re-qualifying the supported PostgreSQL major.
const PostgreSQL18Image = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

const (
	defaultPassword       = "leapview-conformance-secret"
	defaultStartupTimeout = 90 * time.Second
	defaultHarnessTimeout = 3 * time.Minute
)

var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Role describes one explicit PostgreSQL role.  Roles are non-superusers by
// default; Login must be set for a role that will open its own connection.
type Role struct {
	Name     string
	Password string
	Login    bool
}

// Harness owns one PostgreSQL container and an administrator connection to its
// bootstrap database.  Use NewDatabase for a per-test database, and close
// application pools before t.Cleanup runs so DROP DATABASE can be deterministic.
type Harness struct {
	container *tcpostgres.PostgresContainer
	adminURL  string
	rootCert  string
	admin     *pgxpool.Pool

	mu    sync.Mutex
	roles map[string]Role
	// PostgreSQL objects may be owned by explicitly provisioned roles.  Require
	// roles to be registered before databases so LIFO cleanup always drops the
	// database before its owners.
	databaseCreated bool
}

// Required reports whether the real PostgreSQL conformance lane is mandatory.
// When mandatory, an unavailable provider or pinned image fails the test;
// otherwise Start skips the test cleanly.
func Required() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED"))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}

// Start starts a pinned PostgreSQL 18 container.  Docker is optional for local
// runs, but setting LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED makes all startup
// failures fatal (fail closed).
func Start(t *testing.T) *Harness {
	return start(t, false)
}

// StartTLS starts the pinned PostgreSQL 18 image with a disposable
// self-signed certificate. It is intended for production-admission tests,
// whose configuration must require encrypted PostgreSQL URLs even though the
// ordinary conformance harness deliberately exercises plaintext connections.
// Callers should use RootCertPath with sslmode=verify-full when constructing
// URLs for this harness.
func StartTLS(t *testing.T) *Harness {
	return start(t, true)
}

func start(t *testing.T, tls bool) *Harness {
	t.Helper()
	// REQUIRED is the fail-closed lane contract and always wins over a stale
	// skip flag inherited from an outer task or developer shell.
	if shouldSkipConformance() {
		t.Skip("PostgreSQL container tests run in the dedicated conformance lane")
	}
	ctx, cancel := context.WithTimeout(t.Context(), defaultHarnessTimeout)
	defer cancel()
	if !Required() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}

	containerOptions := []testcontainers.ContainerCustomizer{
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword(defaultPassword),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(defaultStartupTimeout)),
		testcontainers.WithLogger(log.TestLogger(t)),
	}
	var rootCert string
	if tls {
		caCert, cert, key := tlsCertificateFiles(t)
		rootCert = caCert
		containerOptions = append(containerOptions,
			tcpostgres.WithSSLCert(caCert, cert, key),
			testcontainers.WithCmd("postgres", "-c", "fsync=off", "-c", "ssl=on", "-c", "ssl_ca_file=/tmp/testcontainers-go/postgres/ca_cert.pem", "-c", "ssl_cert_file=/tmp/testcontainers-go/postgres/server.cert", "-c", "ssl_key_file=/tmp/testcontainers-go/postgres/server.key"),
		)
	}
	container, err := tcpostgres.Run(ctx, PostgreSQL18Image, containerOptions...)
	if err != nil {
		if Required() {
			t.Fatalf("required PostgreSQL 18 conformance container: %v", err)
		}
		t.Skipf("PostgreSQL 18 conformance container unavailable: %v", err)
	}
	// Register cleanup immediately after Run, as recommended by testcontainers;
	// later cleanup callbacks (pools, databases, and roles) run first.
	testcontainers.CleanupContainer(t, container)

	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("PostgreSQL conformance connection string: %v", err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open PostgreSQL conformance administrator pool: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("ping PostgreSQL conformance administrator pool: %v", err)
	}
	h := &Harness{container: container, adminURL: adminURL, rootCert: rootCert, admin: admin, roles: make(map[string]Role)}
	t.Cleanup(func() { admin.Close() })
	return h
}

// RootCertPath returns the host path to the disposable CA that signed the
// harness server certificate. It is empty for a plaintext harness.
func (h *Harness) RootCertPath() string {
	if h == nil {
		return ""
	}
	return h.rootCert
}

func tlsCertificateFiles(t *testing.T) (caCert, cert, key string) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate PostgreSQL conformance CA key: %v", err)
	}
	caTemplate := &x509.Certificate{SerialNumber: bigSerial(t), Subject: pkix.Name{CommonName: "leapview-conformance-ca"}, IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create PostgreSQL conformance CA certificate: %v", err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate PostgreSQL conformance server key: %v", err)
	}
	serverTemplate := &x509.Certificate{SerialNumber: bigSerial(t), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: nil, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create PostgreSQL conformance server certificate: %v", err)
	}
	caCert, cert, key = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key")
	writeTLSFile(t, caCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	writeTLSFile(t, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0o600)
	writeTLSFile(t, key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}), 0o600)
	return caCert, cert, key
}

func bigSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("generate PostgreSQL conformance certificate serial: %v", err)
	}
	return serial
}

func writeTLSFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write PostgreSQL conformance TLS file %q: %v", path, err)
	}
}

func conformanceSkipped() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_SKIP"))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}

func shouldSkipConformance() bool { return conformanceSkipped() && !Required() }

// AdminURL returns a connection URL for the bootstrap administrator.
func (h *Harness) AdminURL() string {
	if h == nil {
		return ""
	}
	return h.adminURL
}

// EnsureRole provisions one non-superuser role.  It is idempotent inside a
// harness, which lets multiple subtests share an explicit role safely.
func (h *Harness) EnsureRole(t *testing.T, role Role) Role {
	t.Helper()
	if h == nil || h.admin == nil {
		t.Fatal("nil PostgreSQL conformance harness")
	}
	if err := validateRole(role); err != nil {
		t.Fatalf("invalid PostgreSQL role: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.roles[role.Name]; ok {
		return existing
	}
	if h.databaseCreated {
		t.Fatalf("PostgreSQL role %q must be provisioned before creating databases", role.Name)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exists, err := platformdb.New(h.admin).RoleExists(ctx, role.Name)
	if err != nil {
		t.Fatalf("check PostgreSQL role %q: %v", role.Name, err)
	}
	if !exists {
		login := "NOLOGIN"
		if role.Login {
			login = "LOGIN"
		}
		statement := fmt.Sprintf("CREATE ROLE %s %s NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT", quoteIdentifier(role.Name), login)
		if role.Password != "" {
			statement += " PASSWORD " + quoteLiteral(role.Password)
		}
		if _, err := h.admin.Exec(ctx, statement); err != nil {
			t.Fatalf("create PostgreSQL role %q: %v", role.Name, err)
		}
	}
	h.roles[role.Name] = role
	// Register after the role exists.  Role cleanup runs after databases (which
	// are registered later), allowing ownership and memberships to disappear.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := h.admin.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+quoteIdentifier(role.Name)); err != nil {
			t.Errorf("drop PostgreSQL role %q: %v", role.Name, err)
		}
	})
	return role
}

// GrantRole grants member membership in parent, which is commonly used to
// let a migration role enter a database owner role.
func (h *Harness) GrantRole(t *testing.T, parent, member Role) {
	t.Helper()
	if err := validateIdentifier(parent.Name); err != nil {
		t.Fatalf("invalid parent PostgreSQL role: %v", err)
	}
	if err := validateIdentifier(member.Name); err != nil {
		t.Fatalf("invalid member PostgreSQL role: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.admin.Exec(ctx, fmt.Sprintf("GRANT %s TO %s", quoteIdentifier(parent.Name), quoteIdentifier(member.Name))); err != nil {
		t.Fatalf("grant PostgreSQL role %q to %q: %v", parent.Name, member.Name, err)
	}
}

// GrantDatabase grants explicit database-level privileges to role.  The
// privilege set is deliberately allow-listed because identifiers cannot be
// parameterized in PostgreSQL DDL.
func (h *Harness) GrantDatabase(t *testing.T, database string, role Role, privileges ...string) {
	t.Helper()
	if h == nil || h.admin == nil {
		t.Fatal("nil PostgreSQL conformance harness")
	}
	if err := validateIdentifier(database); err != nil {
		t.Fatalf("invalid PostgreSQL database: %v", err)
	}
	if err := validateIdentifier(role.Name); err != nil {
		t.Fatalf("invalid PostgreSQL database role: %v", err)
	}
	if len(privileges) == 0 {
		t.Fatal("PostgreSQL database grant requires at least one privilege")
	}
	allowed := map[string]bool{"CONNECT": true, "CREATE": true, "TEMPORARY": true, "TEMP": true}
	for i := range privileges {
		privileges[i] = strings.ToUpper(strings.TrimSpace(privileges[i]))
		if !allowed[privileges[i]] {
			t.Fatalf("unsupported PostgreSQL database privilege %q", privileges[i])
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.admin.Exec(ctx, fmt.Sprintf("GRANT %s ON DATABASE %s TO %s", strings.Join(privileges, ", "), quoteIdentifier(database), quoteIdentifier(role.Name))); err != nil {
		t.Fatalf("grant PostgreSQL database %q to %q: %v", database, role.Name, err)
	}
}

// Database is an isolated database created by a Harness.  Its bootstrap URL
// uses postgres; URL builds a connection URL for an explicitly provisioned role.
type Database struct {
	h     *Harness
	Name  string
	admin *pgxpool.Pool
}

// NewDatabase creates one isolated database and arranges deterministic FORCE
// cleanup.  Roles must be provisioned with EnsureRole before the first
// database; this ordering guarantees role cleanup runs after database cleanup.
// An empty name derives a test-specific name with a digest suffix, while an
// explicit name is retained when valid.
func (h *Harness) NewDatabase(t *testing.T, name string) *Database {
	t.Helper()
	if h == nil || h.admin == nil {
		t.Fatal("nil PostgreSQL conformance harness")
	}
	if name == "" {
		name = generatedDatabaseName(t.Name())
	}
	if err := validateIdentifier(name); err != nil {
		t.Fatalf("invalid PostgreSQL database: %v", err)
	}
	h.mu.Lock()
	h.databaseCreated = true
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.admin.Exec(ctx, "CREATE DATABASE "+quoteIdentifier(name)); err != nil {
		t.Fatalf("create PostgreSQL database %q: %v", name, err)
	}
	targetAdmin, err := pgxpool.New(ctx, h.urlFor(name, "postgres", defaultPassword))
	if err != nil {
		t.Fatalf("open PostgreSQL database administrator pool %q: %v", name, err)
	}
	if err := targetAdmin.Ping(ctx); err != nil {
		targetAdmin.Close()
		t.Fatalf("ping PostgreSQL database administrator pool %q: %v", name, err)
	}
	db := &Database{h: h, Name: name, admin: targetAdmin}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := h.admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+quoteIdentifier(name)+" WITH (FORCE)"); err != nil {
			t.Errorf("drop PostgreSQL database %q: %v", name, err)
		}
	})
	// Close target connections before DROP DATABASE.  This callback is
	// registered second so it runs first under LIFO cleanup; FORCE remains a
	// deterministic fallback for leaked application connections.
	t.Cleanup(func() { targetAdmin.Close() })
	return db
}

// AdminURL returns a bootstrap administrator URL pointed at this database.
func (d *Database) AdminURL() string {
	if d == nil || d.h == nil {
		return ""
	}
	return d.h.urlFor(d.Name, "postgres", defaultPassword)
}

// URL returns a connection URL for role.  The role's credentials are not
// inferred from ambient process configuration.
func (d *Database) URL(role Role) string {
	if d == nil || d.h == nil {
		return ""
	}
	return d.h.urlFor(d.Name, role.Name, role.Password)
}

// CreateSchema creates an isolated schema in this database and grants the
// supplied roles the minimum USAGE/CREATE privileges needed by a test.  The
// returned identifier is safe to interpolate into test-local SQL after it has
// been validated by this method.
func (d *Database) CreateSchema(t *testing.T, name string, roles ...Role) string {
	t.Helper()
	if d == nil || d.h == nil {
		t.Fatal("nil PostgreSQL conformance database")
	}
	if err := validateIdentifier(name); err != nil {
		t.Fatalf("invalid PostgreSQL schema: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := d.admin.Exec(ctx, "CREATE SCHEMA "+quoteIdentifier(name)); err != nil {
		t.Fatalf("create PostgreSQL schema %q: %v", name, err)
	}
	for _, role := range roles {
		if err := validateIdentifier(role.Name); err != nil {
			t.Fatalf("invalid PostgreSQL schema role: %v", err)
		}
		if _, err := d.admin.Exec(ctx, fmt.Sprintf("GRANT USAGE, CREATE ON SCHEMA %s TO %s", quoteIdentifier(name), quoteIdentifier(role.Name))); err != nil {
			t.Fatalf("grant PostgreSQL schema %q to %q: %v", name, role.Name, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := d.admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdentifier(name)+" CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL schema %q: %v", name, err)
		}
	})
	return quoteIdentifier(name)
}

func (h *Harness) urlFor(database, username, password string) string {
	parsed, err := url.Parse(h.adminURL)
	if err != nil {
		return ""
	}
	parsed.Path = "/" + database
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

func generatedDatabaseName(testName string) string {
	digest := sha256.Sum256([]byte(testName + time.Now().UTC().Format(time.RFC3339Nano)))
	return "leapview_test_" + hex.EncodeToString(digest[:])[:24]
}

func validateIdentifier(value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%q must match %s", value, identifierPattern.String())
	}
	return nil
}

func validateRole(role Role) error {
	if err := validateIdentifier(role.Name); err != nil {
		return err
	}
	if role.Login && role.Password == "" {
		return fmt.Errorf("LOGIN role %q requires a non-empty password", role.Name)
	}
	return nil
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func quoteLiteral(value string) string { return `'` + strings.ReplaceAll(value, `'`, `''`) + `'` }
