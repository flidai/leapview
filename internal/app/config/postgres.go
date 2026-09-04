package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

const (
	postgresControlMigratorRole = "leapview_control_migrator"
	postgresControlRuntimeRole  = "leapview_control_runtime"
)

// ValidatePostgres checks the role and intent settings that are part of the
// PostgreSQL control-plane foundation. The migration SQL names these roles
// directly, so accepting aliases only defers a configuration error until
// schema preparation or pool startup.
func (c Config) ValidatePostgres() error {
	for _, role := range []struct {
		name, configured, canonical string
	}{
		{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE", c.PostgresControlMigratorRole, postgresControlMigratorRole},
		{"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE", c.PostgresControlRuntimeRole, postgresControlRuntimeRole},
	} {
		if configured := strings.TrimSpace(role.configured); configured != "" && configured != role.canonical {
			return fmt.Errorf("%s must be %q; custom PostgreSQL role names are not supported by the foundation", role.name, role.canonical)
		}
	}
	if intent := strings.TrimSpace(c.PostgresControlIntent); intent != "" && intent != string(platformpostgres.IntentReadWrite) {
		return fmt.Errorf("LEAPVIEW_POSTGRES_CONTROL_INTENT must be %q; the foundation does not support read-only control access", platformpostgres.IntentReadWrite)
	}
	return nil
}

// ValidatePostgresControlInitialization validates the two independent
// credentials used by production admin initialization. It is deliberately not
// part of generic configuration validation: local/evaluation processes and
// read-only tooling do not own this operation.
func (c Config) ValidatePostgresControlInitialization() error {
	if err := c.ValidatePostgres(); err != nil {
		return err
	}
	runtime, migrator := c.PostgresControlRuntimeConfig(), c.PostgresControlMigratorConfig()
	if strings.TrimSpace(runtime.URL) == "" {
		return errors.New("production admin initialization requires LEAPVIEW_POSTGRES_CONTROL_URL")
	}
	if strings.TrimSpace(migrator.URL) == "" {
		return errors.New("production admin initialization requires LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	}
	if postgresCredentialAlias(runtime.URL, migrator.URL) {
		return errors.New("PostgreSQL control runtime and migrator URLs must use distinct credentials")
	}
	if !c.PostgresRequireTLS {
		return errors.New("production admin initialization requires LEAPVIEW_POSTGRES_REQUIRE_TLS=true")
	}
	if err := runtime.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control runtime configuration: %w", err)
	}
	if err := migrator.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control migrator configuration: %w", err)
	}
	return nil
}

func postgresCredentialAlias(left, right string) bool {
	a, okA := postgresCredentialIdentity(left)
	b, okB := postgresCredentialIdentity(right)
	return okA && okB && a == b
}

func postgresCredentialIdentity(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.User == nil || parsed.Fragment != "" {
		return "", false
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	database, pathErr := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if pathErr != nil || username == "" || !hasPassword || database == "" || strings.Contains(database, "/") {
		return "", false
	}
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", false
		}
	}
	for _, value := range []string{parsed.Hostname(), database, username, password} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", false
		}
	}
	return strings.ToLower(parsed.Hostname()) + "|" + strconv.Itoa(port) + "|" + database + "|" + username + "|" + password, true
}

// PostgresControlRuntimeConfig maps the bounded runtime authority. It never
// contains the owner-capable migration credential.
func (c Config) PostgresControlRuntimeConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlRuntimeRole)
	if role == "" {
		role = postgresControlRuntimeRole
	}
	return platformpostgres.Config{
		URL: c.PostgresControlURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: role, Intent: configuredPostgresIntent(c.PostgresControlIntent),
		RequireTLS: c.PostgresRequireTLS,
		MinConns:   int32(c.PostgresControlPoolMinConns), MaxConns: int32(c.PostgresControlPoolMaxConns),
		AcquireTimeout: c.PostgresControlAcquireTimeout, StatementTimeout: c.PostgresControlStatementTimeout,
		LockTimeout: c.PostgresControlLockTimeout, IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
}

// PostgresControlMigratorConfig maps the one-connection authority used only
// to apply and verify the control-plane schema and bootstrap state.
func (c Config) PostgresControlMigratorConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlMigratorRole)
	if role == "" {
		role = postgresControlMigratorRole
	}
	return platformpostgres.Config{
		URL: c.PostgresControlMigratorURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: role, Intent: platformpostgres.IntentReadWrite, RequireTLS: c.PostgresRequireTLS,
		MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresControlAcquireTimeout, StatementTimeout: c.PostgresControlStatementTimeout,
		LockTimeout: c.PostgresControlLockTimeout, IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
}

func configuredPostgresIntent(raw string) platformpostgres.Intent {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return platformpostgres.IntentReadWrite
	}
	return platformpostgres.Intent(raw)
}
