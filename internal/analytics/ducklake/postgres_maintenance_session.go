package ducklake

// PostgreSQL DuckLake catalog-maintenance session construction.
//
// Physical expiry/cleanup is deliberately not run through the ordinary
// runtime Environment pool. This helper provisions one temporary DuckDB
// PostgreSQL secret and pins one DuckDB connection for the lifetime of the
// maintenance operation. The connection URL is parsed only to construct the
// secret; it is never included in ATTACH SQL or returned in diagnostics.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const (
	// DefaultPostgresCatalogMaintenanceRole is the local/provisioning default.
	// Production callers may choose another role, but it must remain distinct
	// from both runtime and migrator credentials.
	DefaultPostgresCatalogMaintenanceRole = "leapview_ducklake_maintenance"
	DefaultPostgresCatalogRuntimeRole     = "leapview_ducklake_runtime"
	DefaultPostgresCatalogMigratorRole    = "leapview_ducklake_migrator"
	postgresMaintenanceSecret             = "leapview_pg_maintenance"
	duckLakeMaintenanceSecret             = "leapview_lake_maintenance"
)

var (
	ErrPostgresCatalogMaintenanceSession = errors.New("PostgreSQL DuckLake maintenance session is invalid")
	ErrPostgresCatalogMaintenanceURL     = errors.New("PostgreSQL DuckLake maintenance URL is invalid")
)

// PostgresCatalogMaintenanceSessionConfig contains only the identities needed
// to construct the maintenance attach. RuntimeURL and MigratorURL are
// optional comparison evidence; when supplied, reusing either credential is
// rejected even if URL formatting differs.
type PostgresCatalogMaintenanceSessionConfig struct {
	Catalog            PostgresCatalogConfig
	PostgresURL        string
	MaintenanceRole    string
	RuntimeRole        string
	MigratorRole       string
	RuntimeURL         string
	MigratorURL        string
	MemoryMaxBytes     int64
	TempMaxBytes       int64
	MaxThreads         int
	TempDir            string
	DataPath           string
	ExtensionAdmission extension.Admission
}

// Validate checks the dedicated-role and writer-attach contract without
// opening a network connection. It intentionally does not include URL values
// in any returned error.
func (c PostgresCatalogMaintenanceSessionConfig) Validate() error {
	return c.validate(true)
}

// validateIdentity is used by the standalone credential bootstrap. It keeps
// URL/role/catalog identity checks strict without forcing callers that only
// provision a scanner secret to construct a full maintenance resource policy.
func (c PostgresCatalogMaintenanceSessionConfig) validateIdentity() error {
	return c.validate(false)
}

func (c PostgresCatalogMaintenanceSessionConfig) validate(requirePolicy bool) error {
	role := strings.TrimSpace(c.MaintenanceRole)
	if role == "" {
		role = DefaultPostgresCatalogMaintenanceRole
	}
	runtimeRole := strings.TrimSpace(c.RuntimeRole)
	if runtimeRole == "" {
		runtimeRole = DefaultPostgresCatalogRuntimeRole
	}
	migratorRole := strings.TrimSpace(c.MigratorRole)
	if migratorRole == "" {
		migratorRole = DefaultPostgresCatalogMigratorRole
	}
	if !isMaintenanceSQLIdentifier(role) || role == runtimeRole || role == migratorRole || role == DefaultPostgresCatalogRuntimeRole || role == DefaultPostgresCatalogMigratorRole {
		return fmt.Errorf("%w: maintenance role must be dedicated", ErrPostgresCatalogMaintenanceSession)
	}
	if !isMaintenanceSQLIdentifier(runtimeRole) || !isMaintenanceSQLIdentifier(migratorRole) {
		return fmt.Errorf("%w: runtime and migrator roles are invalid", ErrPostgresCatalogMaintenanceSession)
	}
	if strings.TrimSpace(c.PostgresURL) == "" {
		return fmt.Errorf("%w: URL is required", ErrPostgresCatalogMaintenanceURL)
	}
	if requirePolicy {
		if c.MemoryMaxBytes <= 0 || c.TempMaxBytes <= 0 || c.MaxThreads <= 0 {
			return fmt.Errorf("%w: positive memory, temporary-storage, and thread limits are required", ErrPostgresCatalogMaintenanceSession)
		}
		if strings.TrimSpace(c.DataPath) == "" {
			return fmt.Errorf("%w: canonical maintenance data path is required", ErrPostgresCatalogMaintenanceSession)
		}
		if _, err := CanonicalDataPath(c.DataPath); err != nil {
			return fmt.Errorf("%w: maintenance data path is invalid", ErrPostgresCatalogMaintenanceSession)
		}
	}
	if strings.TrimSpace(c.TempDir) != "" && c.TempDir != strings.TrimSpace(c.TempDir) {
		return fmt.Errorf("%w: temporary directory is not normalized", ErrPostgresCatalogMaintenanceSession)
	}
	parsed, err := parseMaintenanceURL(c.PostgresURL, role)
	if err != nil {
		return err
	}
	maintenanceFingerprint := postgresCredentialFingerprint(parsed)
	for _, raw := range []string{c.RuntimeURL, c.MigratorURL} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		other, parseErr := parseMaintenanceURL(raw, "")
		if parseErr != nil {
			return fmt.Errorf("%w: comparison URL is invalid", ErrPostgresCatalogMaintenanceURL)
		}
		if postgresCredentialFingerprint(other) == maintenanceFingerprint {
			return fmt.Errorf("%w: maintenance credentials alias another catalog role", ErrPostgresCatalogMaintenanceSession)
		}
	}
	catalog := c.Catalog
	if catalog.DuckLakeSecret == "" {
		catalog.DuckLakeSecret = duckLakeMaintenanceSecret
	}
	if catalog.PostgresSecret == "" {
		catalog.PostgresSecret = postgresMaintenanceSecret
	}
	if catalog.Mode != PostgresCatalogWriter {
		return fmt.Errorf("%w: catalog must use writer attach mode", ErrPostgresCatalogMaintenanceSession)
	}
	if err := catalog.Validate(); err != nil {
		return fmt.Errorf("%w: catalog attach contract: %v", ErrPostgresCatalogMaintenanceSession, err)
	}
	return nil
}

// PostgresCatalogMaintenanceCredentialBootstrap creates the per-session
// DuckDB PostgreSQL secret and loads the admitted postgres scanner. It is
// useful to callers that own the connector lifecycle themselves.
func PostgresCatalogMaintenanceCredentialBootstrap(c PostgresCatalogMaintenanceSessionConfig) (CredentialBootstrap, error) {
	if err := c.validateIdentity(); err != nil {
		return nil, err
	}
	if c.ExtensionAdmission == nil {
		return nil, fmt.Errorf("%w: extension admission is required", ErrPostgresCatalogMaintenanceSession)
	}
	parsed, err := parseMaintenanceURL(c.PostgresURL, normalizedRole(c.MaintenanceRole, DefaultPostgresCatalogMaintenanceRole))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, execer driver.ExecerContext) error {
		if execer == nil {
			return fmt.Errorf("%w: DuckDB executor is nil", ErrPostgresCatalogMaintenanceSession)
		}
		admitted, err := c.ExtensionAdmission.AdmitExtension(ctx, "postgres")
		if err != nil {
			return fmt.Errorf("%w: admit PostgreSQL scanner", ErrPostgresCatalogMaintenanceSession)
		}
		if err := validateAdmittedExtension(admitted, "postgres"); err != nil {
			return fmt.Errorf("%w: PostgreSQL scanner admission", ErrPostgresCatalogMaintenanceSession)
		}
		if _, err := execer.ExecContext(ctx, "LOAD '"+sqlLiteral(admitted.Path)+"'", nil); err != nil {
			return fmt.Errorf("%w: load PostgreSQL scanner", ErrPostgresCatalogMaintenanceSession)
		}
		statement := postgresSecretStatement(catalogSecretName(c), parsed)
		if _, err := execer.ExecContext(ctx, statement, nil); err != nil {
			return fmt.Errorf("%w: create temporary PostgreSQL secret", ErrPostgresCatalogMaintenanceSession)
		}
		return nil
	}, nil
}

func postgresSecretStatement(name string, parsed parsedMaintenanceURL) string {
	options := []string{
		"TYPE postgres",
		fmt.Sprintf("HOST '%s'", sqlLiteral(parsed.Hostname)),
		fmt.Sprintf("PORT %d", parsed.Port),
		fmt.Sprintf("DATABASE '%s'", sqlLiteral(parsed.Database)),
		fmt.Sprintf("USER '%s'", sqlLiteral(parsed.Username)),
		fmt.Sprintf("PASSWORD '%s'", sqlLiteral(parsed.Password)),
		fmt.Sprintf("SSLMODE '%s'", sqlLiteral(parsed.SSLMode)),
	}
	// Keep the accepted TLS material explicit and bounded. These options are
	// libpq-compatible paths supported by DuckDB's postgres secret; arbitrary
	// URL query parameters are rejected by parseMaintenanceURL.
	for _, option := range []struct {
		name, value string
	}{
		{"SSLROOTCERT", parsed.SSLRootCert},
		{"SSLCERT", parsed.SSLCert},
		{"SSLKEY", parsed.SSLKey},
	} {
		if option.value != "" {
			options = append(options, fmt.Sprintf("%s '%s'", option.name, sqlLiteral(option.value)))
		}
	}
	return fmt.Sprintf("CREATE OR REPLACE TEMPORARY SECRET %s (%s)", quoteCatalogIdentifier(name), strings.Join(options, ", "))
}

// PostgresCatalogMaintenanceSession owns a single DuckDB SQL connection and
// its connector. Callers pass Conn() to NewPostgresCatalogMaintenance and
// must Close this session after the operation completes.
type PostgresCatalogMaintenanceSession struct {
	db   *sql.DB
	conn *sql.Conn
	once sync.Once
	err  error
}

// OpenPostgresCatalogMaintenanceSession opens and pins exactly one DuckDB
// connection. The resulting connection has the existing DuckLake catalog
// attached in writable, non-migrating mode; no pool or second connection is
// created.
func OpenPostgresCatalogMaintenanceSession(ctx context.Context, c PostgresCatalogMaintenanceSessionConfig) (*PostgresCatalogMaintenanceSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// NewConnector stores its initializer and runs it when the first physical
	// connection is opened. Capture the caller context now so admission,
	// secret creation, and ATTACH all observe the same cancellation boundary;
	// the connector callback itself does not receive a context argument.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: initialize connection: %w", ErrPostgresCatalogMaintenanceSession, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.ExtensionAdmission == nil {
		return nil, fmt.Errorf("%w: extension admission is required", ErrPostgresCatalogMaintenanceSession)
	}
	catalog := c.Catalog
	if catalog.DuckLakeSecret == "" {
		catalog.DuckLakeSecret = duckLakeMaintenanceSecret
	}
	if catalog.PostgresSecret == "" {
		catalog.PostgresSecret = postgresMaintenanceSecret
	}
	canonicalDataPath, err := CanonicalDataPath(c.DataPath)
	if err != nil {
		return nil, fmt.Errorf("%w: maintenance data path", ErrPostgresCatalogMaintenanceSession)
	}
	c.DataPath = canonicalDataPath
	if strings.TrimSpace(c.TempDir) != "" {
		if err := securefs.EnsurePrivateDir(c.TempDir); err != nil {
			return nil, fmt.Errorf("%w: prepare temporary directory", ErrPostgresCatalogMaintenanceSession)
		}
	}
	allowedDirectories, err := maintenanceAllowedDirectories(c)
	if err != nil {
		return nil, err
	}
	statements, err := catalog.Statements()
	if err != nil {
		return nil, fmt.Errorf("%w: catalog statements", ErrPostgresCatalogMaintenanceSession)
	}
	bootstrap, err := PostgresCatalogMaintenanceCredentialBootstrap(c)
	if err != nil {
		return nil, err
	}
	initCtx := ctx
	connector, err := duckdb.NewConnector(":memory:", func(execer driver.ExecerContext) error {
		if err := initCtx.Err(); err != nil {
			return fmt.Errorf("%w: initialize connection: %w", ErrPostgresCatalogMaintenanceSession, err)
		}
		for _, statement := range maintenanceResourceStatements(c) {
			if _, err := execer.ExecContext(initCtx, statement, nil); err != nil {
				return fmt.Errorf("%w: configure bounded DuckDB maintenance session", ErrPostgresCatalogMaintenanceSession)
			}
		}
		admitted, err := c.ExtensionAdmission.AdmitExtension(initCtx, "ducklake")
		if err != nil {
			return fmt.Errorf("%w: admit DuckLake extension", ErrPostgresCatalogMaintenanceSession)
		}
		if err := validateAdmittedExtension(admitted, "ducklake"); err != nil {
			return fmt.Errorf("%w: DuckLake extension admission", ErrPostgresCatalogMaintenanceSession)
		}
		if _, err := execer.ExecContext(initCtx, "LOAD '"+sqlLiteral(admitted.Path)+"'", nil); err != nil {
			return fmt.Errorf("%w: load DuckLake extension", ErrPostgresCatalogMaintenanceSession)
		}
		if err := bootstrap(initCtx, execer); err != nil {
			return err
		}
		for _, statement := range statements {
			if _, err := execer.ExecContext(initCtx, statement, nil); err != nil {
				return fmt.Errorf("%w: attach catalog", ErrPostgresCatalogMaintenanceSession)
			}
		}
		if _, err := execer.ExecContext(initCtx, "USE "+catalogAlias, nil); err != nil {
			return fmt.Errorf("%w: select catalog", ErrPostgresCatalogMaintenanceSession)
		}
		// The admitted scanners are loaded and the PostgreSQL-backed catalog is
		// attached before external access is disabled. This preserves the
		// connector's already-authorized I/O while preventing subsequent
		// extension installation, autoloading, or ad hoc external access.
		for _, statement := range []string{allowedDirectories, "SET enable_external_access = false", "SET lock_configuration = true"} {
			if _, err := execer.ExecContext(initCtx, statement, nil); err != nil {
				return fmt.Errorf("%w: lock DuckDB maintenance configuration", ErrPostgresCatalogMaintenanceSession)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create connector", ErrPostgresCatalogMaintenanceSession)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: initialize connection", ErrPostgresCatalogMaintenanceSession)
	}
	return &PostgresCatalogMaintenanceSession{db: db, conn: conn}, nil
}

func maintenanceResourceStatements(c PostgresCatalogMaintenanceSessionConfig) []string {
	statements := []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		fmt.Sprintf("SET memory_limit = '%dB'", c.MemoryMaxBytes),
		fmt.Sprintf("SET max_temp_directory_size = '%dB'", c.TempMaxBytes),
		fmt.Sprintf("SET threads = %d", c.MaxThreads),
	}
	if strings.TrimSpace(c.TempDir) != "" {
		statements = append(statements, "SET temp_directory = '"+sqlLiteral(c.TempDir)+"'")
	}
	return statements
}

func maintenanceAllowedDirectories(c PostgresCatalogMaintenanceSessionConfig) (string, error) {
	// DuckDB checks this allow-list whenever external access is disabled. The
	// values are canonicalized and escaped before they reach SQL. Include the
	// private temporary directory when configured so bounded spill files remain
	// usable after lockdown.
	dataPath, err := CanonicalDataPath(c.DataPath)
	if err != nil {
		return "", fmt.Errorf("%w: maintenance data path", ErrPostgresCatalogMaintenanceSession)
	}
	directories := []string{dataPath}
	if strings.TrimSpace(c.TempDir) != "" {
		tempDir, err := filepath.Abs(c.TempDir)
		if err != nil {
			return "", fmt.Errorf("%w: maintenance temporary directory", ErrPostgresCatalogMaintenanceSession)
		}
		directories = append(directories, filepath.Clean(tempDir))
	}
	quoted := make([]string, len(directories))
	for i, directory := range directories {
		quoted[i] = "'" + sqlLiteral(directory) + "'"
	}
	return "SET allowed_directories = [" + strings.Join(quoted, ", ") + "]", nil
}

// Conn returns the pinned connection as the narrow maintenance executor.
func (s *PostgresCatalogMaintenanceSession) Conn() CatalogMaintenanceConnection {
	if s == nil {
		return nil
	}
	return s.conn
}

// Close releases the pinned connection and connector. It is idempotent.
func (s *PostgresCatalogMaintenanceSession) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.conn != nil {
			s.err = s.conn.Close()
		}
		if s.db != nil {
			s.err = errors.Join(s.err, s.db.Close())
		}
	})
	return s.err
}

type parsedMaintenanceURL struct {
	Hostname, Database, Username, Password, SSLMode string
	SSLRootCert, SSLCert, SSLKey                    string
	Port                                            int
}

func parseMaintenanceURL(raw, expectedRole string) (parsedMaintenanceURL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.Fragment != "" {
		return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
	}
	query := parsed.Query()
	for key, values := range query {
		if key != "sslmode" && key != "sslrootcert" && key != "sslcert" && key != "sslkey" {
			return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
		}
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
		}
		if strings.ContainsAny(values[0], "\x00\r\n") {
			return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
		}
	}
	if parsed.User == nil {
		return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username == "" || !hasPassword || password == "" || strings.Trim(parsed.Path, "/") == "" || strings.Contains(strings.TrimPrefix(parsed.Path, "/"), "/") {
		return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
	}
	if expectedRole != "" && username != expectedRole {
		return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceSession
	}
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
		}
	}
	database, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil || database == "" || strings.ContainsAny(database, "\x00\r\n") {
		return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
	}
	for _, value := range []string{parsed.Hostname(), database, username, password} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
		}
	}
	sslMode := strings.TrimSpace(query.Get("sslmode"))
	if sslMode == "" {
		sslMode = "require"
	}
	if sslMode != "require" && sslMode != "verify-ca" && sslMode != "verify-full" && sslMode != "disable" {
		return parsedMaintenanceURL{}, ErrPostgresCatalogMaintenanceURL
	}
	return parsedMaintenanceURL{
		Hostname: parsed.Hostname(), Database: database, Username: username, Password: password,
		Port: port, SSLMode: sslMode, SSLRootCert: strings.TrimSpace(query.Get("sslrootcert")),
		SSLCert: strings.TrimSpace(query.Get("sslcert")), SSLKey: strings.TrimSpace(query.Get("sslkey")),
	}, nil
}

func postgresCredentialFingerprint(parsed parsedMaintenanceURL) string {
	return strings.ToLower(parsed.Hostname) + "|" + strconv.Itoa(parsed.Port) + "|" + parsed.Database + "|" + parsed.Username + "|" + parsed.Password
}

func normalizedRole(role, fallback string) string {
	if strings.TrimSpace(role) == "" {
		return fallback
	}
	return strings.TrimSpace(role)
}

func catalogSecretName(c PostgresCatalogMaintenanceSessionConfig) string {
	if strings.TrimSpace(c.Catalog.PostgresSecret) != "" {
		return strings.TrimSpace(c.Catalog.PostgresSecret)
	}
	return postgresMaintenanceSecret
}

func isMaintenanceSQLIdentifier(value string) bool {
	return catalogIdentifierPattern.MatchString(value) && value == strings.TrimSpace(value)
}
