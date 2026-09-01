package composectl

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// seedQualificationNativeEnvironment copies the packaged application
// environment example into the qualification workspace.  Compose resolves
// leapview.env while creating the network, so this seed must exist before the
// network preparation phase.  The destination is always written atomically
// with private permissions; no values are synthesized here.
func seedQualificationNativeEnvironment(bundleRoot string) error {
	bundleRoot = strings.TrimSpace(bundleRoot)
	if bundleRoot == "" {
		return errors.New("qualification environment bundle root is required")
	}
	root, err := filepath.Abs(bundleRoot)
	if err != nil {
		return fmt.Errorf("resolve qualification environment bundle root: %w", err)
	}
	source := filepath.Join(root, "leapview.env.example")
	if err := requireNonEmptyFile(source); err != nil {
		return fmt.Errorf("qualification application environment example: %w", err)
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read qualification application environment example: %w", err)
	}
	if len(contents) == 0 {
		return fmt.Errorf("qualification application environment example is empty: %s", source)
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(root, appEnvName), contents); err != nil {
		return fmt.Errorf("seed qualification application environment: %w", err)
	}
	return nil
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
	return map[string]string{
		"LEAPVIEW_POSTGRES_CONTROL_URL":               topology.ControlURL,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL":      topology.ControlMigratorURL,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE":     qualificationNativePostgresControlMigratorRole,
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE":      qualificationNativePostgresControlRuntimeRole,
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL":   topology.ControlMaintenanceURL,
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE":  qualificationNativePostgresControlMaintenanceRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_URL":              topology.DuckLakeURL,
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE":     qualificationNativePostgresDuckLakeRuntimeRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL":  topology.DuckLakeMaintenanceURL,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE": qualificationNativePostgresDuckLakeMaintenanceRole,
		"LEAPVIEW_POSTGRES_REQUIRE_TLS":               "true",
	}, nil
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
	if strings.TrimSpace(environmentPath) == "" {
		return errors.New("qualification application environment path is required")
	}
	if err := requireNonEmptyFile(environmentPath); err != nil {
		return fmt.Errorf("qualification application environment: %w", err)
	}
	if err := updateEnvFile(environmentPath, values); err != nil {
		return fmt.Errorf("write qualification PostgreSQL serving environment: %w", err)
	}
	// A packaged example does not contain operation URLs.  Remove any stale
	// values if a caller supplied a pre-existing file, ensuring the serving
	// environment can never persist owner-capable credentials.
	return removeQualificationNativeOperationURLs(environmentPath)
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
	if _, err := validateQualificationNativePostgresURL(
		topology.DuckLakeMigratorURL,
		qualificationNativePostgresDuckLakeMigratorRole,
		qualificationNativePostgresDuckLakeDatabase,
		"DuckLake migrator",
	); err != nil {
		return nil, err
	}
	return map[string]string{
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL":  topology.DuckLakeMigratorURL,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE": qualificationNativePostgresDuckLakeMigratorRole,
	}, nil
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
	seenURLs := make(map[string]string, len(urls))
	seenPasswords := make(map[string]string, len(urls))
	for _, connection := range urls {
		value := strings.TrimSpace(connection.value)
		if value == "" && !connection.required {
			continue
		}
		identity, err := validateQualificationNativePostgresURL(value, connection.role, connection.database, connection.name)
		if err != nil {
			return err
		}
		if previous, exists := seenURLs[identity]; exists {
			return fmt.Errorf("qualification PostgreSQL %s URL aliases %s URL", connection.name, previous)
		}
		seenURLs[identity] = connection.name
		parsed, _ := url.Parse(value)
		password, _ := parsed.User.Password()
		if previous, exists := seenPasswords[password]; exists {
			return fmt.Errorf("qualification PostgreSQL %s credential aliases %s credential", connection.name, previous)
		}
		seenPasswords[password] = connection.name
	}
	return nil
}

func validateQualificationNativePostgresURL(raw, expectedRole, expectedDatabase, label string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.User == nil {
		return "", fmt.Errorf("qualification PostgreSQL %s URL is malformed", label)
	}
	if parsed.Path != "/"+expectedDatabase {
		return "", fmt.Errorf("qualification PostgreSQL %s URL targets an unexpected database", label)
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username != expectedRole || !hasPassword || password == "" {
		return "", fmt.Errorf("qualification PostgreSQL %s URL has an invalid role identity", label)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query["sslmode"]) != 1 || !strings.EqualFold(strings.TrimSpace(query.Get("sslmode")), "require") {
		return "", fmt.Errorf("qualification PostgreSQL %s URL must set sslmode=require", label)
	}
	port := 5432
	if parsed.Port() != "" {
		parsedPort, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", fmt.Errorf("qualification PostgreSQL %s URL has an invalid port", label)
		}
		port = parsedPort
	}
	for _, component := range []string{parsed.Hostname(), expectedDatabase, username, password} {
		if strings.ContainsAny(component, "\x00\r\n") {
			return "", fmt.Errorf("qualification PostgreSQL %s URL contains invalid characters", label)
		}
	}
	return strings.ToLower(parsed.Hostname()) + "|" + strconv.Itoa(port) + "|" + expectedDatabase + "|" + username + "|" + password, nil
}

func removeQualificationNativeOperationURLs(environmentPath string) error {
	contents, err := os.ReadFile(environmentPath)
	if err != nil {
		return err
	}
	forbidden := map[string]struct{}{
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL":           {},
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL": {},
	}
	lines := strings.Split(string(contents), "\n")
	filtered := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		name, _, present := strings.Cut(line, "=")
		if present {
			if _, remove := forbidden[name]; remove {
				changed = true
				continue
			}
		}
		filtered = append(filtered, line)
	}
	if !changed {
		return nil
	}
	return securefs.WritePrivateFileAtomic(environmentPath, []byte(strings.Join(filtered, "\n")))
}
