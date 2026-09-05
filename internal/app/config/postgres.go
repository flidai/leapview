package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

const (
	postgresControlMigratorRole           = "leapview_control_migrator"
	postgresControlRuntimeRole            = "leapview_control_runtime"
	postgresControlReadonlyRole           = "leapview_control_readonly"
	postgresControlMaintenanceRole        = "leapview_control_maintenance"
	postgresControlUpgradeCoordinatorRole = "leapview_control_upgrade_coordinator"
	postgresDuckLakeMigratorRole          = "leapview_ducklake_migrator"
	postgresDuckLakeRuntimeRole           = "leapview_ducklake_runtime"
	postgresDuckLakeMaintenanceRole       = "leapview_ducklake_maintenance"
)

// postgresCredentialAlias compares only the credential identity carried by a
// PostgreSQL URL. Query ordering, TLS options, and an omitted default port do
// not create a new credential, so they must not evade production role
// separation checks. It deliberately returns false for malformed URLs; the
// owning pool validator reports malformed connection details later without
// echoing the URL (or its password) in this package's diagnostics.
func postgresCredentialAlias(left, right string) bool {
	a, okA := postgresCredentialIdentity(left)
	b, okB := postgresCredentialIdentity(right)
	return okA && okB && a == b
}

func postgresCredentialIdentity(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.User == nil {
		return "", false
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username == "" || !hasPassword {
		return "", false
	}
	database, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil || database == "" || strings.Contains(database, "/") {
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

// PostgresControlPlaneConfig maps the explicit control-plane role settings to
// the capability-neutral pool policy. Maintenance is always represented as a
// required one-connection read/write pool; production validation rejects an
// empty URL rather than synthesizing credentials from another role. The
// readonly pool is omitted when its URL is empty.
func (c Config) PostgresControlPlaneConfig() platformpostgres.ControlPlaneConfig {
	migratorRole := strings.TrimSpace(c.PostgresControlMigratorRole)
	if migratorRole == "" {
		migratorRole = postgresControlMigratorRole
	}
	runtimeRole := strings.TrimSpace(c.PostgresControlRuntimeRole)
	if runtimeRole == "" {
		runtimeRole = postgresControlRuntimeRole
	}
	readonlyRole := strings.TrimSpace(c.PostgresControlReadonlyRole)
	if readonlyRole == "" {
		readonlyRole = postgresControlReadonlyRole
	}
	config := platformpostgres.ControlPlaneConfig{
		Migrator: platformpostgres.Config{
			URL:                    c.PostgresControlMigratorURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            migratorRole,
			Intent:                 platformpostgres.IntentReadWrite,
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               1,
			MaxConns:               1,
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		},
		Runtime: platformpostgres.Config{
			URL:                    c.PostgresControlURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            runtimeRole,
			Intent:                 platformpostgres.IntentReadWrite,
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               int32(c.PostgresControlPoolMinConns),
			MaxConns:               int32(c.PostgresControlPoolMaxConns),
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		},
		Maintenance: c.PostgresControlMaintenanceConfig(),
	}
	if strings.TrimSpace(c.PostgresControlReadonlyURL) != "" {
		readonly := platformpostgres.Config{
			URL:                    c.PostgresControlReadonlyURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            readonlyRole,
			Intent:                 platformpostgres.IntentReadOnly,
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               1,
			MaxConns:               4,
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		}
		config.Readonly = &readonly
	}
	return config
}

// PostgresDuckLakeRuntimeConfig maps only the ordinary catalog runtime role.
// Catalog migration credentials are deliberately absent from serve
// configuration: catalog upgrades run as a separately fenced operation.
func (c Config) PostgresDuckLakeRuntimeConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresDuckLakeRuntimeRole)
	if role == "" {
		role = postgresDuckLakeRuntimeRole
	}
	return platformpostgres.Config{
		URL:                    c.PostgresDuckLakeURL,
		ExpectedMajor:          c.PostgresExpectedMajor,
		RuntimeRole:            role,
		Intent:                 platformpostgres.IntentReadWrite,
		RequireTLS:             c.PostgresRequireTLS,
		MinConns:               int32(c.PostgresDuckLakePoolMinConns),
		MaxConns:               int32(c.PostgresDuckLakePoolMaxConns),
		AcquireTimeout:         c.PostgresDuckLakeAcquireTimeout,
		StatementTimeout:       c.PostgresDuckLakeStatementTimeout,
		LockTimeout:            c.PostgresDuckLakeLockTimeout,
		IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
}

// PostgresDuckLakeMaintenanceConfig describes the separately authenticated,
// one-connection read/write pool used only by bounded physical DuckLake
// expiry and cleanup. Ordinary serving must never open or borrow this
// credential.
func (c Config) PostgresDuckLakeMaintenanceConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresDuckLakeMaintenanceRole)
	if role == "" {
		role = postgresDuckLakeMaintenanceRole
	}
	return platformpostgres.Config{
		URL: c.PostgresDuckLakeMaintenanceURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: role, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresDuckLakeAcquireTimeout, StatementTimeout: c.PostgresDuckLakeStatementTimeout,
		LockTimeout: c.PostgresDuckLakeLockTimeout, IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
}

// PostgresDuckLakeUpgradeConfig returns the two explicitly independent pools
// used by the catalog upgrade operation. Serving never opens owner-capable
// catalog credentials or the guarded control coordinator pool.
func (c Config) PostgresDuckLakeUpgradeConfig() (platformpostgres.Config, platformpostgres.Config) {
	coordinatorRole := strings.TrimSpace(c.PostgresControlUpgradeCoordinatorRole)
	if coordinatorRole == "" {
		coordinatorRole = postgresControlUpgradeCoordinatorRole
	}
	catalogRole := strings.TrimSpace(c.PostgresDuckLakeMigratorRole)
	if catalogRole == "" {
		catalogRole = postgresDuckLakeMigratorRole
	}
	coordinator := platformpostgres.Config{
		URL: c.PostgresControlUpgradeCoordinatorURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: coordinatorRole, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresControlAcquireTimeout, StatementTimeout: c.PostgresControlStatementTimeout,
		LockTimeout: c.PostgresControlLockTimeout, IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
	catalog := platformpostgres.Config{
		URL: c.PostgresDuckLakeMigratorURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: catalogRole, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresDuckLakeAcquireTimeout, StatementTimeout: c.PostgresDuckLakeStatementTimeout,
		LockTimeout: c.PostgresDuckLakeLockTimeout, IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
	return coordinator, catalog
}

// PostgresControlMaintenanceConfig describes the separately authenticated,
// one-connection read/write maintenance pool. It is intentionally kept out of
// the ordinary runtime policy; composition retains this bounded pool so
// explicit maintenance operations can be wired without borrowing runtime
// credentials.
func (c Config) PostgresControlMaintenanceConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlMaintenanceRole)
	if role == "" {
		role = postgresControlMaintenanceRole
	}
	return platformpostgres.Config{
		URL: c.PostgresControlMaintenanceURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: role, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresControlAcquireTimeout, StatementTimeout: c.PostgresControlStatementTimeout,
		LockTimeout: c.PostgresControlLockTimeout, IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
}

// ValidatePostgresUpgrade enforces the explicit two-credential operation
// contract. It is called by an upgrade command, never by serving startup, so
// an ordinary runtime process cannot accidentally open owner-capable pools.
func (c Config) ValidatePostgresUpgrade() error {
	if err := c.validateCanonicalPostgresRoles("upgrade"); err != nil {
		return err
	}
	coordinator, catalog := c.PostgresDuckLakeUpgradeConfig()
	if err := coordinator.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL upgrade coordinator configuration: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake migrator configuration: %w", err)
	}
	if coordinator.RuntimeRole == catalog.RuntimeRole {
		return errors.New("PostgreSQL upgrade coordinator and DuckLake migrator roles must be distinct")
	}
	if strings.TrimSpace(coordinator.URL) == strings.TrimSpace(c.PostgresControlURL) || strings.TrimSpace(coordinator.URL) == strings.TrimSpace(c.PostgresControlMigratorURL) || strings.TrimSpace(coordinator.URL) == strings.TrimSpace(c.PostgresDuckLakeURL) {
		return errors.New("PostgreSQL upgrade coordinator URL must use distinct credentials")
	}
	if strings.TrimSpace(catalog.URL) == strings.TrimSpace(c.PostgresDuckLakeURL) || strings.TrimSpace(catalog.URL) == strings.TrimSpace(c.PostgresControlURL) || strings.TrimSpace(catalog.URL) == strings.TrimSpace(c.PostgresControlMigratorURL) {
		return errors.New("PostgreSQL DuckLake migrator URL must use distinct credentials")
	}
	if c.Production && !c.PostgresRequireTLS {
		return errors.New("production PostgreSQL upgrade requires TLS")
	}
	return nil
}

// ValidatePostgresProduction enforces the clean-slate production startup
// contract. Production serving carries no migrator credential; an optional
// readonly URL is validated when supplied. This method is kept
// separate from Config.Validate so embedded development/test fixtures can
// continue to use their approved local SQLite cache without weakening the
// production serve command's fail-closed check.
func (c Config) ValidatePostgresProduction() error {
	if !c.Production {
		return nil
	}
	return c.validatePostgresServeTarget("production", true, true, false)
}

// ValidatePostgresDevelopment validates the local PostgreSQL composition used
// by `task dev`. Development keeps the same capability/database and role
// boundaries as production, but deliberately permits loopback TLS-disabled
// URLs and does not require an admitted delivery pool. The latter is supplied
// by an explicit bootstrap operation when a developer publishes a candidate.
func (c Config) ValidatePostgresDevelopment() error {
	return c.validatePostgresServeTarget("development", false, false, true)
}

// validatePostgresServeTarget contains the common control/DuckLake connection
// contract. Keep production's error wording stable because it is part of the
// startup diagnostics consumed by deployment tooling; development uses the
// same checks with a mode-specific prefix.
func (c Config) validatePostgresServeTarget(mode string, requireTLS, requireDeliveryPool, requireMigrator bool) error {
	require := func(value, name string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s serve requires %s", mode, name)
		}
		return nil
	}
	for _, required := range [][2]string{
		{c.PostgresControlURL, "LEAPVIEW_POSTGRES_CONTROL_URL"},
		{c.PostgresDuckLakeURL, "LEAPVIEW_POSTGRES_DUCKLAKE_URL"},
		{c.PostgresControlMaintenanceURL, "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL"},
		{c.PostgresDuckLakeMaintenanceURL, "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL"},
	} {
		if err := require(required[0], required[1]); err != nil {
			return err
		}
	}
	if requireMigrator {
		if err := require(c.PostgresControlMigratorURL, "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL"); err != nil {
			return err
		}
	}
	if requireTLS && !c.PostgresRequireTLS {
		return fmt.Errorf("%s serve requires LEAPVIEW_POSTGRES_REQUIRE_TLS=true", mode)
	}
	if err := c.validateCanonicalPostgresRoles(mode); err != nil {
		return err
	}
	if !c.PostgresRequireTLS {
		for _, connection := range []struct {
			name string
			url  string
		}{
			{"LEAPVIEW_POSTGRES_CONTROL_URL", c.PostgresControlURL},
			{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL", c.PostgresControlMigratorURL},
			{"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL", c.PostgresControlMaintenanceURL},
			{"LEAPVIEW_POSTGRES_CONTROL_READONLY_URL", c.PostgresControlReadonlyURL},
			{"LEAPVIEW_POSTGRES_DUCKLAKE_URL", c.PostgresDuckLakeURL},
			{"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL", c.PostgresDuckLakeMigratorURL},
			{"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL", c.PostgresDuckLakeMaintenanceURL},
		} {
			if strings.TrimSpace(connection.url) == "" {
				continue
			}
			parsed, err := url.Parse(strings.TrimSpace(connection.url))
			if err != nil || parsed == nil || parsed.Hostname() == "" {
				return fmt.Errorf("%s serve requires %s to target loopback when TLS is disabled", mode, connection.name)
			}
			host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
			if host != "localhost" && host != "127.0.0.1" && host != "::1" {
				return fmt.Errorf("%s serve requires %s to target loopback when TLS is disabled", mode, connection.name)
			}
		}
	}
	control := c.PostgresControlPlaneConfig()
	ducklake := c.PostgresDuckLakeRuntimeConfig()
	ducklakeMaintenance := c.PostgresDuckLakeMaintenanceConfig()
	if control.Migrator.RuntimeRole == control.Runtime.RuntimeRole {
		return fmt.Errorf("%s control migrator and runtime roles must be distinct", mode)
	}
	if strings.TrimSpace(control.Migrator.URL) != "" && postgresCredentialAlias(control.Migrator.URL, control.Runtime.URL) {
		return fmt.Errorf("%s control migrator and runtime URLs must use distinct credentials", mode)
	}
	if ducklake.RuntimeRole == control.Runtime.RuntimeRole || ducklake.RuntimeRole == control.Migrator.RuntimeRole || ducklake.RuntimeRole == control.Maintenance.RuntimeRole || ducklakeMaintenance.RuntimeRole == control.Runtime.RuntimeRole || ducklakeMaintenance.RuntimeRole == control.Migrator.RuntimeRole || ducklakeMaintenance.RuntimeRole == control.Maintenance.RuntimeRole {
		return fmt.Errorf("%s DuckLake and control PostgreSQL roles must be distinct", mode)
	}
	if postgresCredentialAlias(ducklake.URL, control.Runtime.URL) || postgresCredentialAlias(ducklake.URL, control.Migrator.URL) || postgresCredentialAlias(ducklake.URL, control.Maintenance.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, control.Runtime.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, control.Migrator.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, control.Maintenance.URL) {
		return fmt.Errorf("%s DuckLake and control PostgreSQL URLs must use distinct database credentials", mode)
	}
	if strings.TrimSpace(control.Migrator.URL) != "" {
		if err := control.Migrator.Validate(); err != nil {
			return fmt.Errorf("invalid PostgreSQL control migrator configuration: %w", err)
		}
	}
	if err := control.Runtime.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control runtime configuration: %w", err)
	}
	if control.Maintenance.RuntimeRole == control.Runtime.RuntimeRole || control.Maintenance.RuntimeRole == control.Migrator.RuntimeRole {
		return fmt.Errorf("%s control maintenance role must be distinct from runtime and migrator roles", mode)
	}
	if control.Maintenance.RuntimeRole != postgresControlMaintenanceRole {
		return fmt.Errorf("%s control maintenance role must be %q because the baseline grants only that fixed least-privilege role", mode, postgresControlMaintenanceRole)
	}
	if postgresCredentialAlias(control.Maintenance.URL, control.Runtime.URL) || postgresCredentialAlias(control.Maintenance.URL, control.Migrator.URL) {
		return fmt.Errorf("%s control maintenance URL must use distinct credentials", mode)
	}
	if err := control.Maintenance.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control maintenance configuration: %w", err)
	}
	if control.Readonly != nil {
		if control.Readonly.RuntimeRole == control.Runtime.RuntimeRole || control.Readonly.RuntimeRole == control.Migrator.RuntimeRole || control.Readonly.RuntimeRole == control.Maintenance.RuntimeRole || control.Readonly.RuntimeRole == ducklake.RuntimeRole || control.Readonly.RuntimeRole == ducklakeMaintenance.RuntimeRole {
			return fmt.Errorf("%s control readonly role must be distinct from runtime, migrator, maintenance, and DuckLake roles", mode)
		}
		if postgresCredentialAlias(control.Readonly.URL, control.Runtime.URL) || postgresCredentialAlias(control.Readonly.URL, control.Migrator.URL) || postgresCredentialAlias(control.Readonly.URL, control.Maintenance.URL) || postgresCredentialAlias(control.Readonly.URL, ducklake.URL) || postgresCredentialAlias(control.Readonly.URL, ducklakeMaintenance.URL) {
			return fmt.Errorf("%s control readonly URL must use distinct credentials", mode)
		}
		if err := control.Readonly.Validate(); err != nil {
			return fmt.Errorf("invalid PostgreSQL control readonly configuration: %w", err)
		}
	}
	if err := ducklake.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake runtime configuration: %w", err)
	}
	if ducklakeMaintenance.RuntimeRole == ducklake.RuntimeRole || ducklakeMaintenance.RuntimeRole == strings.TrimSpace(c.PostgresDuckLakeMigratorRole) || ducklakeMaintenance.RuntimeRole == "leapview_ducklake_migrator" {
		return fmt.Errorf("%s DuckLake maintenance role must be distinct from runtime and migrator roles", mode)
	}
	if postgresCredentialAlias(ducklakeMaintenance.URL, ducklake.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, c.PostgresDuckLakeMigratorURL) {
		return fmt.Errorf("%s DuckLake maintenance URL must use distinct credentials", mode)
	}
	if err := ducklakeMaintenance.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake maintenance configuration: %w", err)
	}
	if requireDeliveryPool {
		physicalPoolID := strings.TrimSpace(c.DeliveryPhysicalPoolID)
		if physicalPoolID == "" || physicalPoolID != c.DeliveryPhysicalPoolID || len(physicalPoolID) > 255 {
			return errors.New("production serve requires a canonical LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID")
		}
		if err := platformdigest.ValidateSHA256Identity(c.DeliveryPhysicalPoolCompatibilityDigest); err != nil {
			return fmt.Errorf("production serve requires LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST: %w", err)
		}
	}
	if requireTLS {
		// Keep operation-only URLs under the same production TLS contract as
		// serving pools. They are not opened by ordinary serving, but accepting
		// sslmode=require or verify-ca here would leave a production migration or
		// maintenance path without certificate-and-hostname authentication.
		for _, connection := range []struct {
			name string
			url  string
		}{
			{"LEAPVIEW_POSTGRES_CONTROL_URL", c.PostgresControlURL},
			{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL", c.PostgresControlMigratorURL},
			{"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL", c.PostgresControlUpgradeCoordinatorURL},
			{"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL", c.PostgresControlMaintenanceURL},
			{"LEAPVIEW_POSTGRES_CONTROL_READONLY_URL", c.PostgresControlReadonlyURL},
			{"LEAPVIEW_POSTGRES_DUCKLAKE_URL", c.PostgresDuckLakeURL},
			{"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL", c.PostgresDuckLakeMigratorURL},
			{"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL", c.PostgresDuckLakeMaintenanceURL},
		} {
			if strings.TrimSpace(connection.url) == "" {
				continue
			}
			if err := validatePostgresCertificateHostnameTLS(connection.url); err != nil {
				return fmt.Errorf("%s serve requires %s to use sslmode=verify-full: %w", mode, connection.name, err)
			}
		}
	}
	return nil
}

func validatePostgresCertificateHostnameTLS(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return errors.New("PostgreSQL URL is malformed")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return errors.New("PostgreSQL URL is malformed")
	}
	sslModes := query["sslmode"]
	if len(sslModes) != 1 || strings.TrimSpace(sslModes[0]) != "verify-full" {
		return errors.New("certificate and hostname verification is required")
	}
	return nil
}

// validateCanonicalPostgresRoles keeps the environment contract truthful: the
// baseline and its role policy provision a fixed set of durable authority
// roles. Accepting arbitrary names here would produce pools that cannot be
// admitted or granted the capability-owned privileges.
func (c Config) validateCanonicalPostgresRoles(mode string) error {
	roles := []struct {
		name string
		got  string
		want string
	}{
		{"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE", c.PostgresControlRuntimeRole, postgresControlRuntimeRole},
		{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE", c.PostgresControlMigratorRole, postgresControlMigratorRole},
		{"LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE", c.PostgresControlReadonlyRole, postgresControlReadonlyRole},
		{"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE", c.PostgresControlMaintenanceRole, postgresControlMaintenanceRole},
		{"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_ROLE", c.PostgresControlUpgradeCoordinatorRole, postgresControlUpgradeCoordinatorRole},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE", c.PostgresDuckLakeRuntimeRole, postgresDuckLakeRuntimeRole},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE", c.PostgresDuckLakeMigratorRole, postgresDuckLakeMigratorRole},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE", c.PostgresDuckLakeMaintenanceRole, postgresDuckLakeMaintenanceRole},
	}
	for _, role := range roles {
		if got := strings.TrimSpace(role.got); got != "" && got != role.want {
			return fmt.Errorf("%s requires %s=%q because only provisioned PostgreSQL roles are supported", mode, role.name, role.want)
		}
	}
	return nil
}
