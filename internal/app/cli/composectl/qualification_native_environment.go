package composectl

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

func qualificationNativeFirstInstallOptions(
	topology *qualificationNativePostgresTopology,
	artifacts physicalPoolBootstrapArtifacts,
) (FirstInstallOptions, error) {
	if err := validateQualificationNativePostgresEnvironmentTopology(topology); err != nil {
		return FirstInstallOptions{}, err
	}
	if err := validatePhysicalPoolBootstrapArtifacts(artifacts); err != nil {
		return FirstInstallOptions{}, err
	}
	options := FirstInstallOptions{
		Profile: FirstInstallPostgresExternal,
		Postgres: FirstInstallPostgres{
			ControlURL:             topology.ControlURL,
			ControlMigratorURL:     topology.ControlMigratorURL,
			ControlMaintenanceURL:  topology.ControlMaintenanceURL,
			DuckLakeURL:            topology.DuckLakeURL,
			DuckLakeMaintenanceURL: topology.DuckLakeMaintenanceURL,
			DuckLakeMigratorURL:    topology.DuckLakeMigratorURL,
		},
		PhysicalPool: FirstInstallPhysicalPool{
			Pool: artifacts.Pool,
			Evidence: physicalpool.EvidenceArtifact{
				SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion,
				Evidence:      artifacts.Evidence,
			},
		},
	}
	if err := options.Validate(); err != nil {
		return FirstInstallOptions{}, err
	}
	return options, nil
}

// seedQualificationNativeEnvironment copies the packaged application
// environment example into the qualification bundle. Compose resolves
// leapview.env while creating the network, so this seed must exist before the
// network preparation phase.  The destination is always written atomically
// with private permissions; no values are synthesized here.
func seedQualificationNativeEnvironment(bundleRoot string) error {
	return seedApplicationEnvironment(bundleRoot)
}

// seedQualificationNativePostgresEnvironment is a Controller convenience
// seam used by qualification orchestration.  It intentionally does not call
// Compose or initialize the instance; callers seed before network creation.
func (c *Controller) seedQualificationNativePostgresEnvironment() error {
	if c == nil {
		return errors.New("controller is required")
	}
	return seedQualificationNativeEnvironment(c.root)
}

// qualificationNativePostgresServingEnvironment contains only the
// production serving keys.  Owner-capable operation URLs are intentionally
// absent: they are supplied to the one bootstrap operation through the
// narrow map returned below and never persisted in leapview.env.
func qualificationNativePostgresServingEnvironment(
	topology *qualificationNativePostgresTopology,
) (map[string]string, error) {
	if err := validateQualificationNativePostgresEnvironmentTopology(topology); err != nil {
		return nil, err
	}
	values, err := postgresServingEnvironment(
		topology.ControlURL, topology.ControlMaintenanceURL,
		topology.DuckLakeURL, topology.DuckLakeMaintenanceURL,
	)
	if err != nil {
		return nil, err
	}
	values["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE"] = postgresControlMigratorRole
	if strings.TrimSpace(topology.ControlReadonlyURL) != "" {
		readonlyURL, err := canonicalPostgresConnectionURL(postgresConnection{
			name: "control readonly", value: topology.ControlReadonlyURL,
			role:     qualificationNativePostgresControlReadonlyRole,
			database: qualificationNativePostgresControlDatabase,
		})
		if err != nil {
			return nil, err
		}
		values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"] = readonlyURL
		values["LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"] = qualificationNativePostgresControlReadonlyRole
	}
	return values, nil
}

// writeQualificationNativePostgresEnvironment replaces the serving values in
// leapview.env using the existing env-file updater.  Initialization-owned
// secrets (CSRF and metrics tokens), placeholders, and all unrelated settings
// remain untouched.  The updater performs a private atomic write.
func writeQualificationNativePostgresEnvironment(
	environmentPath string,
	topology *qualificationNativePostgresTopology,
) error {
	values, err := qualificationNativePostgresServingEnvironment(topology)
	if err != nil {
		return err
	}
	const readonlyURLKey = "LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"
	const readonlyRoleKey = "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"
	readonlyURL, hasReadonlyURL := values[readonlyURLKey]
	readonlyRole, hasReadonlyRole := values[readonlyRoleKey]
	delete(values, readonlyURLKey)
	delete(values, readonlyRoleKey)
	if hasReadonlyURL != hasReadonlyRole {
		return errors.New("qualification PostgreSQL readonly serving URL and role must be configured together")
	}
	if err := writePostgresServingEnvironment(
		environmentPath, values,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
	); err != nil {
		return err
	}
	if hasReadonlyURL {
		if err := appendOrReplaceEnvFile(environmentPath, readonlyURLKey, readonlyURL); err != nil {
			return fmt.Errorf("write qualification PostgreSQL readonly serving URL: %w", err)
		}
		if err := appendOrReplaceEnvFile(environmentPath, readonlyRoleKey, readonlyRole); err != nil {
			return fmt.Errorf("write qualification PostgreSQL readonly serving role: %w", err)
		}
	} else if err := removePostgresEnvironmentURLs(environmentPath, readonlyURLKey, readonlyRoleKey); err != nil {
		return fmt.Errorf("remove qualification PostgreSQL readonly serving credentials: %w", err)
	}
	return nil
}

// qualificationNativePostgresOperationEnvironment returns the only values
// allowed to cross the later bootstrap-command boundary.  It deliberately
// excludes the control upgrade-coordinator URL and every serving value.
func qualificationNativePostgresOperationEnvironment(
	topology *qualificationNativePostgresTopology,
) (map[string]string, error) {
	if err := validateQualificationNativePostgresEnvironmentTopology(topology); err != nil {
		return nil, err
	}
	if topology == nil || strings.TrimSpace(topology.DuckLakeMigratorURL) == "" {
		return nil, errors.New("qualification PostgreSQL DuckLake migrator URL is required")
	}
	return postgresPoolBootstrapEnvironment(topology.ControlMigratorURL, topology.DuckLakeMigratorURL)
}

func (c *Controller) writeQualificationNativePostgresEnvironment(
	topology *qualificationNativePostgresTopology,
) error {
	if c == nil {
		return errors.New("controller is required")
	}
	return writeQualificationNativePostgresEnvironment(c.path(appEnvName), topology)
}

func validateQualificationNativePostgresEnvironmentTopology(
	topology *qualificationNativePostgresTopology,
) error {
	if topology == nil {
		return errors.New("qualification PostgreSQL topology is required")
	}
	// Validate every role supplied by the topology, including operation-only
	// identities when present.  Persisted serving roles are always fixed and
	// cannot be replaced by an operator-provided alias.
	roles := []struct {
		name     string
		value    string
		expected string
		required bool
	}{
		{"control runtime", topology.ControlRuntimeRole, qualificationNativePostgresControlRuntimeRole, true},
		{"control readonly", topology.ControlReadonlyRole, qualificationNativePostgresControlReadonlyRole, false},
		{"control migrator", topology.ControlMigratorRole, qualificationNativePostgresControlMigratorRole, true},
		{"control upgrade coordinator", topology.ControlUpgradeCoordinatorRole, qualificationNativePostgresControlUpgradeRole, false},
		{"control maintenance", topology.ControlMaintenanceRole, qualificationNativePostgresControlMaintenanceRole, true},
		{"DuckLake runtime", topology.DuckLakeRuntimeRole, qualificationNativePostgresDuckLakeRuntimeRole, true},
		{"DuckLake migrator", topology.DuckLakeMigratorRole, qualificationNativePostgresDuckLakeMigratorRole, false},
		{"DuckLake maintenance", topology.DuckLakeMaintenanceRole, qualificationNativePostgresDuckLakeMaintenanceRole, true},
	}
	seenRoles := make(map[string]string, len(roles))
	for _, role := range roles {
		value := strings.TrimSpace(role.value)
		if value == "" && !role.required {
			continue
		}
		if err := validateQualificationNativePostgresIdentifier(value, "qualification PostgreSQL "+role.name+" role"); err != nil {
			return err
		}
		if value != role.expected {
			return fmt.Errorf("qualification PostgreSQL %s role must be %q", role.name, role.expected)
		}
		if previous, exists := seenRoles[value]; exists {
			return fmt.Errorf("qualification PostgreSQL %s role aliases %s role", role.name, previous)
		}
		seenRoles[value] = role.name
	}

	urls := []struct {
		name     string
		value    string
		role     string
		database string
		required bool
	}{
		{"control runtime", topology.ControlURL, qualificationNativePostgresControlRuntimeRole, qualificationNativePostgresControlDatabase, true},
		{"control readonly", topology.ControlReadonlyURL, qualificationNativePostgresControlReadonlyRole, qualificationNativePostgresControlDatabase, false},
		{"control migrator", topology.ControlMigratorURL, qualificationNativePostgresControlMigratorRole, qualificationNativePostgresControlDatabase, true},
		{"control upgrade coordinator", topology.ControlUpgradeCoordinatorURL, qualificationNativePostgresControlUpgradeRole, qualificationNativePostgresControlDatabase, false},
		{"control maintenance", topology.ControlMaintenanceURL, qualificationNativePostgresControlMaintenanceRole, qualificationNativePostgresControlDatabase, true},
		{"DuckLake runtime", topology.DuckLakeURL, qualificationNativePostgresDuckLakeRuntimeRole, qualificationNativePostgresDuckLakeDatabase, true},
		{"DuckLake migrator", topology.DuckLakeMigratorURL, qualificationNativePostgresDuckLakeMigratorRole, qualificationNativePostgresDuckLakeDatabase, false},
		{"DuckLake maintenance", topology.DuckLakeMaintenanceURL, qualificationNativePostgresDuckLakeMaintenanceRole, qualificationNativePostgresDuckLakeDatabase, true},
	}
	connections := make([]postgresConnection, 0, len(urls))
	for _, connection := range urls {
		value := strings.TrimSpace(connection.value)
		if value == "" && !connection.required {
			continue
		}
		connections = append(connections, postgresConnection{
			name: connection.name, value: value, role: connection.role, database: connection.database,
		})
	}
	return validateDistinctPostgresConnections(connections)
}

func validateQualificationNativePostgresURL(raw, expectedRole, expectedDatabase, label string) (string, error) {
	identity, _, err := validatePostgresConnection(postgresConnection{
		name: "qualification " + label, value: raw, role: expectedRole, database: expectedDatabase,
	})
	return identity, err
}

// assertQualificationNativeServingCredentialBoundary verifies that the
// persisted serving environment contains no owner-capable operation URLs.
// Catalog migrations receive those credentials through one-shot process
// environment forwarding; retaining either value in leapview.env would make
// it available to every long-running serving process.
func assertQualificationNativeServingCredentialBoundary(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("qualification application environment path is required")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read qualification application environment: %w", err)
	}
	trackedKeys := map[string]struct{}{
		"LEAPVIEW_POSTGRES_REQUIRE_TLS":          {},
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE": {}, "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE": {},
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE": {}, "LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE": {},
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE": {}, "LEAPVIEW_POSTGRES_CONTROL_URL": {},
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_URL": {}, "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE": {},
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL": {}, "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL": {},
		"LEAPVIEW_POSTGRES_DUCKLAKE_URL": {}, "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL": {},
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL": {}, "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL": {},
	}
	seenKeys := make(map[string]struct{}, len(trackedKeys))
	for _, line := range strings.Split(string(contents), "\n") {
		name, _, present := strings.Cut(line, "=")
		if _, tracked := trackedKeys[name]; !present || !tracked {
			continue
		}
		if _, duplicate := seenKeys[name]; duplicate {
			return fmt.Errorf("qualification serving environment contains duplicate key %s", name)
		}
		seenKeys[name] = struct{}{}
	}
	const readonlyURLKey = "LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"
	const readonlyRoleKey = "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"
	_, hasReadonlyURL := seenKeys[readonlyURLKey]
	_, hasReadonlyRole := seenKeys[readonlyRoleKey]
	if hasReadonlyURL != hasReadonlyRole {
		return errors.New("qualification serving environment must configure the readonly URL and role together")
	}
	values := environmentValues(string(contents))
	if !strings.EqualFold(strings.TrimSpace(values["LEAPVIEW_POSTGRES_REQUIRE_TLS"]), "true") {
		return errors.New("qualification serving environment must require PostgreSQL TLS")
	}
	roles := map[string]string{
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE":      qualificationNativePostgresControlRuntimeRole,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE":     qualificationNativePostgresControlMigratorRole,
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE":  qualificationNativePostgresControlMaintenanceRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE":     qualificationNativePostgresDuckLakeRuntimeRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE": qualificationNativePostgresDuckLakeMaintenanceRole,
	}
	if hasReadonlyRole {
		roles[readonlyRoleKey] = qualificationNativePostgresControlReadonlyRole
	}
	for key, expected := range roles {
		if strings.TrimSpace(values[key]) != expected {
			return fmt.Errorf("qualification serving environment role %s must be %q", key, expected)
		}
	}
	urls := []struct {
		key, role, database string
	}{
		{"LEAPVIEW_POSTGRES_CONTROL_URL", qualificationNativePostgresControlRuntimeRole, qualificationNativePostgresControlDatabase},
		{"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL", qualificationNativePostgresControlMaintenanceRole, qualificationNativePostgresControlDatabase},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_URL", qualificationNativePostgresDuckLakeRuntimeRole, qualificationNativePostgresDuckLakeDatabase},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL", qualificationNativePostgresDuckLakeMaintenanceRole, qualificationNativePostgresDuckLakeDatabase},
	}
	connections := make([]postgresConnection, 0, len(urls))
	for _, connection := range urls {
		connections = append(connections, postgresConnection{
			name: connection.key, value: values[connection.key], role: connection.role, database: connection.database,
		})
	}
	if hasReadonlyURL {
		connections = append(connections, postgresConnection{
			name: "control readonly", value: values[readonlyURLKey],
			role: qualificationNativePostgresControlReadonlyRole, database: qualificationNativePostgresControlDatabase,
		})
	}
	if err := validateDistinctPostgresConnections(connections); err != nil {
		return err
	}
	for _, key := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
	} {
		if _, present := values[key]; present {
			return fmt.Errorf("qualification serving environment contains operation-only credential %s", key)
		}
	}
	return nil
}

func removeQualificationNativeOperationURLs(environmentPath string) error {
	return removePostgresEnvironmentURLs(
		environmentPath,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
	)
}
