// Package postgresducklake owns the application adapter that supplies
// ephemeral PostgreSQL and object-store credentials to one DuckDB connector.
package postgresducklake

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	DuckLakeSecret = "leapview_lake"
	PostgresSecret = "leapview_pg"
)

type CredentialConfig struct {
	PostgresURL string
	// AllowPlaintextLoopback is an explicit development-only policy. TLS may
	// be disabled for the worktree-scoped loopback PostgreSQL helper, but
	// callers must opt in; a loopback URL alone never weakens production
	// transport requirements.
	AllowPlaintextLoopback bool
	Contract               *ducklake.PoolContract
	ExtensionAdmission     extension.Admission
	S3                     gcadapter.S3Config
}

// NewCredentialBootstrap validates one explicit PostgreSQL credential and
// returns a per-connector bootstrap. It works for either the ordinary runtime
// credential or the separately authenticated catalog migrator; callers choose
// the URL and never expose it through the returned DuckLake configuration.
func NewCredentialBootstrap(config CredentialConfig) (ducklake.CredentialBootstrap, error) {
	if config.ExtensionAdmission == nil {
		return nil, errors.New("PostgreSQL DuckLake credential bootstrap requires extension admission")
	}
	parsed, err := url.Parse(strings.TrimSpace(config.PostgresURL))
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
		return nil, errors.New("PostgreSQL DuckLake URL is invalid")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("PostgreSQL DuckLake URL contains an invalid fragment")
	}
	query := parsed.Query()
	for key, values := range query {
		if key != "sslmode" && key != "sslrootcert" {
			return nil, fmt.Errorf("PostgreSQL DuckLake URL query option %q is unsupported", key)
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("PostgreSQL DuckLake URL query option %q must be specified once", key)
		}
	}
	user, password := "", ""
	if parsed.User != nil {
		user = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	if user == "" || password == "" || strings.Trim(parsed.Path, "/") == "" {
		return nil, errors.New("PostgreSQL DuckLake URL requires database, user, and password")
	}
	for name, value := range map[string]string{"host": parsed.Hostname(), "user": user, "password": password} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("PostgreSQL DuckLake URL %s contains a control character", name)
		}
	}
	port := 5432
	if rawPort := parsed.Port(); rawPort != "" {
		port, err = strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("PostgreSQL DuckLake URL port is invalid")
		}
	}
	database, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || !strings.HasPrefix(database, "/") || strings.TrimPrefix(database, "/") == "" || strings.Contains(strings.TrimPrefix(database, "/"), "/") {
		return nil, errors.New("PostgreSQL DuckLake URL database path is invalid")
	}
	database = strings.TrimPrefix(database, "/")
	if strings.ContainsAny(database, "\x00\r\n") {
		return nil, errors.New("PostgreSQL DuckLake database identity is invalid")
	}
	sslMode := "require"
	if configured := strings.TrimSpace(query.Get("sslmode")); configured != "" {
		sslMode = configured
	}
	if sslMode != "require" && sslMode != "verify-ca" && sslMode != "verify-full" && !(sslMode == "disable" && config.AllowPlaintextLoopback && postgresLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("PostgreSQL DuckLake credential bootstrap requires TLS")
	}
	sslRootCert := strings.TrimSpace(query.Get("sslrootcert"))
	if sslRootCert != "" {
		if !filepath.IsAbs(sslRootCert) || filepath.Clean(sslRootCert) != sslRootCert || strings.ContainsAny(sslRootCert, "\x00\r\n") {
			return nil, errors.New("PostgreSQL DuckLake URL sslrootcert must be a canonical absolute path")
		}
		if sslMode != "verify-ca" && sslMode != "verify-full" {
			return nil, errors.New("PostgreSQL DuckLake URL sslrootcert requires certificate verification")
		}
	}

	var objectBootstrap ducklake.CredentialBootstrap
	if config.Contract != nil {
		config.S3.ExtensionAdmission = config.ExtensionAdmission
		objectBootstrap, err = gcadapter.NewPoolCredentialBootstrap(config.Contract, config.S3)
		if err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context, execer driver.ExecerContext) error {
		if execer == nil {
			return errors.New("DuckDB credential bootstrap executor is nil")
		}
		admitted, err := config.ExtensionAdmission.AdmitExtension(ctx, "postgres")
		if err != nil {
			return fmt.Errorf("admit PostgreSQL DuckDB scanner: %w", err)
		}
		base := filepath.Base(admitted.Path)
		stem := strings.TrimSuffix(base, ".duckdb_extension")
		expectedStem := extension.ArtifactFilenameStem("postgres")
		if admitted.Name != "postgres" || strings.TrimSpace(admitted.Identity) == "" || strings.TrimSpace(admitted.Version) == "" || strings.TrimSpace(admitted.Platform) == "" || platformdigest.ValidateSHA256Identity(admitted.Digest) != nil || !filepath.IsAbs(admitted.Path) || filepath.Clean(admitted.Path) != admitted.Path || !strings.HasSuffix(base, ".duckdb_extension") || (stem != expectedStem && !strings.HasPrefix(stem, expectedStem+"-")) {
			return errors.New("PostgreSQL DuckDB scanner admission returned an invalid absolute artifact")
		}
		if _, err := execer.ExecContext(ctx, "LOAD '"+sqlLiteral(admitted.Path)+"'", nil); err != nil {
			return fmt.Errorf("load PostgreSQL DuckDB scanner: %w", err)
		}
		rootCertOption := ""
		if sslRootCert != "" {
			rootCertOption = ", SSLROOTCERT '" + sqlLiteral(sslRootCert) + "'"
		}
		statement := fmt.Sprintf("CREATE OR REPLACE TEMPORARY SECRET %s (TYPE postgres, HOST '%s', PORT %d, DATABASE '%s', USER '%s', PASSWORD '%s', SSLMODE '%s'%s)", PostgresSecret, sqlLiteral(parsed.Hostname()), port, sqlLiteral(database), sqlLiteral(user), sqlLiteral(password), sqlLiteral(sslMode), rootCertOption)
		if _, err := execer.ExecContext(ctx, statement, nil); err != nil {
			return fmt.Errorf("create temporary PostgreSQL DuckDB secret: %w", err)
		}
		if objectBootstrap != nil {
			return objectBootstrap(ctx, execer)
		}
		return nil
	}, nil
}

func sqlLiteral(value string) string { return strings.ReplaceAll(value, "'", "''") }

func postgresLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
