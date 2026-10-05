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

const (
	postgresControlDatabase  = "leapview_control"
	postgresDuckLakeDatabase = "leapview_ducklake"

	postgresControlRuntimeRole      = "leapview_control_runtime"
	postgresControlMigratorRole     = "leapview_control_migrator"
	postgresControlMaintenanceRole  = "leapview_control_maintenance"
	postgresDuckLakeRuntimeRole     = "leapview_ducklake_runtime"
	postgresDuckLakeMigratorRole    = "leapview_ducklake_migrator"
	postgresDuckLakeMaintenanceRole = "leapview_ducklake_maintenance"
)

type postgresConnection struct {
	name     string
	value    string
	role     string
	database string
}

// validatePostgresConnection applies the provider PostgreSQL contract shared
// by production installation and native qualification. TLS must verify both
// the CA chain and hostname; operators may use sslrootcert or trusted system
// roots without a fixed deployment-specific certificate path.
func validatePostgresConnection(connection postgresConnection) (string, string, error) {
	if strings.ContainsAny(connection.value, "\x00\r\n") {
		return "", "", fmt.Errorf("PostgreSQL %s URL contains invalid characters", connection.name)
	}
	value := strings.TrimSpace(connection.value)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.User == nil {
		return "", "", fmt.Errorf("PostgreSQL %s URL is malformed", connection.name)
	}
	if parsed.Path != "/"+connection.database {
		return "", "", fmt.Errorf("PostgreSQL %s URL targets an unexpected database", connection.name)
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username != connection.role || !hasPassword || password == "" {
		return "", "", fmt.Errorf("PostgreSQL %s URL has an invalid role identity", connection.name)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", "", fmt.Errorf("PostgreSQL %s URL has an invalid query", connection.name)
	}
	seenQueryNames := make(map[string]struct{}, len(query))
	for key, values := range query {
		normalizedKey := strings.ToLower(key)
		if _, duplicate := seenQueryNames[normalizedKey]; duplicate || len(values) != 1 {
			return "", "", fmt.Errorf("PostgreSQL %s URL repeats a query parameter", connection.name)
		}
		seenQueryNames[normalizedKey] = struct{}{}
		switch normalizedKey {
		case "user", "password", "dbname", "host", "hostaddr", "port", "service", "servicefile", "passfile":
			return "", "", fmt.Errorf("PostgreSQL %s URL query may not override connection identity", connection.name)
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\x00\r\n") {
				return "", "", fmt.Errorf("PostgreSQL %s URL contains invalid characters", connection.name)
			}
		}
	}
	if len(query["sslmode"]) != 1 || strings.TrimSpace(query["sslmode"][0]) != "verify-full" {
		return "", "", fmt.Errorf("PostgreSQL %s URL must set sslmode=verify-full", connection.name)
	}
	port := 5432
	if parsed.Port() != "" {
		parsedPort, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", "", fmt.Errorf("PostgreSQL %s URL has an invalid port", connection.name)
		}
		port = parsedPort
	}
	for _, component := range []string{parsed.Hostname(), connection.database, username, password} {
		if strings.ContainsAny(component, "\x00\r\n") {
			return "", "", fmt.Errorf("PostgreSQL %s URL contains invalid characters", connection.name)
		}
	}
	identity := strings.ToLower(parsed.Hostname()) + "|" + strconv.Itoa(port) + "|" + connection.database + "|" + username + "|" + password
	return identity, password, nil
}

func validateDistinctPostgresConnections(connections []postgresConnection) error {
	seenIdentities := make(map[string]string, len(connections))
	seenPasswords := make(map[string]string, len(connections))
	for _, connection := range connections {
		identity, password, err := validatePostgresConnection(connection)
		if err != nil {
			return err
		}
		if previous, exists := seenIdentities[identity]; exists {
			return fmt.Errorf("PostgreSQL %s URL aliases %s URL", connection.name, previous)
		}
		seenIdentities[identity] = connection.name
		if previous, exists := seenPasswords[password]; exists {
			return fmt.Errorf("PostgreSQL %s credential aliases %s credential", connection.name, previous)
		}
		seenPasswords[password] = connection.name
	}
	return nil
}

func canonicalPostgresConnectionURL(connection postgresConnection) (string, error) {
	if _, _, err := validatePostgresConnection(connection); err != nil {
		return "", err
	}
	parsed, err := url.Parse(strings.TrimSpace(connection.value))
	if err != nil {
		return "", fmt.Errorf("PostgreSQL %s URL is malformed", connection.name)
	}
	password, _ := parsed.User.Password()
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("PostgreSQL %s URL has an invalid query", connection.name)
	}
	parsed.User = url.UserPassword(connection.role, password)
	parsed.RawQuery = query.Encode()
	canonical := parsed.String()
	userInfo := parsed.User.String()
	// Docker Compose interpolates dollar expressions in unquoted env_file
	// values. Encode literal dollars in URL userinfo so credentials arrive
	// unchanged in both Compose and the PostgreSQL URL parser.
	canonical = strings.Replace(canonical, userInfo+"@", strings.ReplaceAll(userInfo, "$", "%24")+"@", 1)
	return canonical, nil
}

func postgresServingEnvironment(controlURL, controlMaintenanceURL, duckLakeURL, duckLakeMaintenanceURL string) (map[string]string, error) {
	connections := []postgresConnection{
		{"control runtime", controlURL, postgresControlRuntimeRole, postgresControlDatabase},
		{"control maintenance", controlMaintenanceURL, postgresControlMaintenanceRole, postgresControlDatabase},
		{"DuckLake runtime", duckLakeURL, postgresDuckLakeRuntimeRole, postgresDuckLakeDatabase},
		{"DuckLake maintenance", duckLakeMaintenanceURL, postgresDuckLakeMaintenanceRole, postgresDuckLakeDatabase},
	}
	canonical := make([]string, len(connections))
	for index, connection := range connections {
		value, err := canonicalPostgresConnectionURL(connection)
		if err != nil {
			return nil, err
		}
		canonical[index] = value
	}
	return map[string]string{
		"LEAPVIEW_POSTGRES_CONTROL_URL":               canonical[0],
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE":      postgresControlRuntimeRole,
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL":   canonical[1],
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE":  postgresControlMaintenanceRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_URL":              canonical[2],
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE":     postgresDuckLakeRuntimeRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL":  canonical[3],
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE": postgresDuckLakeMaintenanceRole,
		"LEAPVIEW_POSTGRES_REQUIRE_TLS":               "true",
	}, nil
}

func postgresPoolBootstrapEnvironment(controlMigratorURL, duckLakeMigratorURL string) (map[string]string, error) {
	controlURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "control migrator", value: controlMigratorURL,
		role: postgresControlMigratorRole, database: postgresControlDatabase,
	})
	if err != nil {
		return nil, err
	}
	duckLakeURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "DuckLake migrator", value: duckLakeMigratorURL,
		role: postgresDuckLakeMigratorRole, database: postgresDuckLakeDatabase,
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL":   controlURL,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE":  postgresControlMigratorRole,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL":  duckLakeURL,
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE": postgresDuckLakeMigratorRole,
	}, nil
}

func seedApplicationEnvironment(bundleRoot string) error {
	bundleRoot = strings.TrimSpace(bundleRoot)
	if bundleRoot == "" {
		return errors.New("application environment bundle root is required")
	}
	root, err := filepath.Abs(bundleRoot)
	if err != nil {
		return fmt.Errorf("resolve application environment bundle root: %w", err)
	}
	source := filepath.Join(root, "leapview.env.example")
	if err := requireNonEmptyFile(source); err != nil {
		return fmt.Errorf("application environment example: %w", err)
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read application environment example: %w", err)
	}
	if len(contents) == 0 {
		return fmt.Errorf("application environment example is empty: %s", source)
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(root, appEnvName), contents); err != nil {
		return fmt.Errorf("seed application environment: %w", err)
	}
	return nil
}

func writePostgresServingEnvironment(path string, values map[string]string, operationURLKeys ...string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("PostgreSQL serving environment path is required")
	}
	if err := requireNonEmptyFile(path); err != nil {
		return fmt.Errorf("PostgreSQL serving environment: %w", err)
	}
	if err := updateEnvFile(path, values); err != nil {
		return fmt.Errorf("write PostgreSQL serving environment: %w", err)
	}
	return removePostgresEnvironmentURLs(path, operationURLKeys...)
}

func appendOrReplaceEnvFile(path, key, value string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("environment file path is required")
	}
	if err := validateEnvLineValue(key, value); err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(contents), "\n")
	found := false
	for index, line := range lines {
		name, _, present := strings.Cut(line, "=")
		if present && name == key {
			lines[index] = key + "=" + value
			found = true
		}
	}
	if !found {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, key+"="+value, "")
	}
	return securefs.WritePrivateFileAtomic(path, []byte(strings.Join(lines, "\n")))
}

func removePostgresEnvironmentURLs(path string, keys ...string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	forbidden := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		forbidden[key] = struct{}{}
	}
	lines := strings.Split(string(contents), "\n")
	filtered := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		name, _, present := strings.Cut(line, "=")
		if _, remove := forbidden[name]; present && remove {
			changed = true
			continue
		}
		filtered = append(filtered, line)
	}
	if !changed {
		return nil
	}
	return securefs.WritePrivateFileAtomic(path, []byte(strings.Join(filtered, "\n")))
}
