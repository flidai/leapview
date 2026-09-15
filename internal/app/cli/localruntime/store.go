package localruntime

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/ociref"
	"golang.org/x/mod/semver"
)

const (
	stateFileName         = "state.json"
	runtimeEnvFileName    = "runtime.env"
	credentialsFileName   = "initial-credentials.json"
	qualificationFileName = "physical-pool-qualification.json"
	poolFileName          = "physical-pool.json"
	evidenceFileName      = "physical-pool-evidence.json"
	manifestFileName      = "runtime-package.json"
	composeFileName       = "compose.yaml"
	postgresInitName      = "postgres-init.sh"
	manifestSchemaName    = "runtime-package.schema.json"
)

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve canonical checkout: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("checkout root %q is not a directory", canonical)
	}
	return filepath.Clean(canonical), nil
}

func discoverCheckoutRoot(start string) (string, error) {
	current, err := canonicalDirectory(start)
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Lstat(filepath.Join(current, ".git")); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return current, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect checkout marker: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return canonicalDirectory(start)
		}
		current = parent
	}
}

func checkoutIdentity(root string) (string, string, error) {
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	return canonical, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func stateDirectory(root, checkoutID string) string {
	return filepath.Join(root, strings.TrimPrefix(checkoutID, "sha256:"))
}

func loadManifest(root string, identity buildinfo.Identity) (runtimeManifest, string, error) {
	root, err := canonicalDirectory(root)
	if err != nil {
		return runtimeManifest{}, "", fmt.Errorf("resolve local runtime package: %w", err)
	}
	payload := make(map[string][]byte, 4)
	for _, name := range []string{manifestFileName, manifestSchemaName, composeFileName, postgresInitName} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return runtimeManifest{}, "", fmt.Errorf("inspect local runtime package %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			return runtimeManifest{}, "", fmt.Errorf("local runtime package %s must be a regular file without group/other write access", name)
		}
		if name == postgresInitName && info.Mode().Perm()&0o100 == 0 {
			return runtimeManifest{}, "", errors.New("local runtime PostgreSQL initializer must be owner-executable")
		}
		payload[name], err = os.ReadFile(path)
		if err != nil {
			return runtimeManifest{}, "", fmt.Errorf("read local runtime package %s: %w", name, err)
		}
	}
	encoded := payload[manifestFileName]
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var manifest runtimeManifest
	if err := decoder.Decode(&manifest); err != nil {
		return runtimeManifest{}, "", fmt.Errorf("decode local runtime manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runtimeManifest{}, "", errors.New("decode local runtime manifest: trailing JSON value")
	}
	if manifest.SchemaVersion != manifestSchemaVersion || manifest.PersistentStateSchemaVersion != persistentStateSchemaVersion {
		return runtimeManifest{}, "", fmt.Errorf("unsupported local runtime manifest/state schema %d/%d", manifest.SchemaVersion, manifest.PersistentStateSchemaVersion)
	}
	minimum := strings.TrimSpace(manifest.ComposeMinimumVersion)
	if !semver.IsValid("v" + minimum) {
		return runtimeManifest{}, "", fmt.Errorf("invalid minimum Compose version %q", minimum)
	}
	if identity.Development || identity.Dirty || identity.Version == buildinfo.DevelopmentVersion {
		return runtimeManifest{}, "", errors.New("local runtime package requires a clean released leapview CLI")
	}
	if manifest.LeapView.Version != identity.Version || manifest.LeapView.Revision != identity.Revision {
		return runtimeManifest{}, "", fmt.Errorf("local runtime package identifies LeapView %s/%s, CLI identifies %s/%s", manifest.LeapView.Version, manifest.LeapView.Revision, identity.Version, identity.Revision)
	}
	if err := ociref.ValidateImmutable(manifest.LeapView.Image); err != nil {
		return runtimeManifest{}, "", fmt.Errorf("validate local LeapView image: %w", err)
	}
	if manifest.Postgres.Major != postgresMajor || manifest.Postgres.Image != postgresImage {
		return runtimeManifest{}, "", errors.New("local runtime package does not identify the supported PostgreSQL image")
	}
	hash := sha256.New()
	for _, name := range []string{manifestFileName, manifestSchemaName, composeFileName, postgresInitName} {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", name, len(payload[name]))
		_, _ = hash.Write(payload[name])
	}
	return manifest, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func loadState(path string) (State, bool, error) {
	encoded, err := securefs.ReadPrivateFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read local runtime state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, false, fmt.Errorf("decode local runtime state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return State{}, false, errors.New("decode local runtime state: trailing JSON value")
	}
	if state.SchemaVersion != stateSchemaVersion {
		return State{}, false, fmt.Errorf("unsupported local runtime state schema %d; explicit upgrade is required", state.SchemaVersion)
	}
	return state, true, nil
}

func saveState(path string, state State) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return securefs.WritePrivateFileAtomic(path, encoded)
}

func randomToken(prefix string, bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("select local application port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func initialEnvironment(state State, manifest runtimeManifest) (map[string]string, error) {
	values := map[string]string{
		"LEAPVIEW_IMAGE":             manifest.LeapView.Image,
		"LEAPVIEW_LOCAL_APP_PORT":    strconv.Itoa(state.Network.AppPort),
		"LEAPVIEW_LOCAL_CHECKOUT_ID": state.Checkout.ID,
		"LEAPVIEW_LOCAL_OWNER_ID":    state.Runtime.OwnerID,
	}
	for _, name := range []string{
		"LEAPVIEW_POSTGRES_BOOTSTRAP_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD",
		"LEAPVIEW_CSRF_KEY",
	} {
		value, err := randomToken("", 32)
		if err != nil {
			return nil, fmt.Errorf("generate local runtime secret: %w", err)
		}
		values[name] = value
	}
	return values, nil
}

func readEnvironment(path string) (map[string]string, error) {
	encoded, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for index, line := range strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\r\n") {
			return nil, fmt.Errorf("invalid private runtime environment line %d", index+1)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate private runtime environment key %q", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("invalid private runtime environment value for %q", name)
		}
		values[name] = value
	}
	return values, nil
}

func writeEnvironment(path string, values map[string]string) error {
	names := make([]string, 0, len(values))
	for name, value := range values {
		if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name+value, "\r\n\x00") {
			return errors.New("local runtime environment contains an invalid key or value")
		}
		names = append(names, name)
	}
	slicesSort(names)
	var out strings.Builder
	for _, name := range names {
		fmt.Fprintf(&out, "%s=%s\n", name, values[name])
	}
	return securefs.WritePrivateFileAtomic(path, []byte(out.String()))
}

func validateRetainedEnvironment(state State, manifest runtimeManifest, values map[string]string) error {
	requiredSecrets := []string{
		"LEAPVIEW_POSTGRES_BOOTSTRAP_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD",
		"LEAPVIEW_CSRF_KEY",
	}
	want := map[string]string{
		"LEAPVIEW_IMAGE":             manifest.LeapView.Image,
		"LEAPVIEW_LOCAL_APP_PORT":    strconv.Itoa(state.Network.AppPort),
		"LEAPVIEW_LOCAL_CHECKOUT_ID": state.Checkout.ID,
		"LEAPVIEW_LOCAL_OWNER_ID":    state.Runtime.OwnerID,
	}
	for name, expected := range want {
		if values[name] != expected {
			return fmt.Errorf("retained local runtime environment disagrees on %s; refusing mutation", name)
		}
	}
	for _, name := range requiredSecrets {
		if values[name] == "" {
			return fmt.Errorf("retained local runtime secret %s is unavailable; refusing replacement", name)
		}
	}
	allowed := len(want) + len(requiredSecrets)
	hasPoolEnvironment := values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"] != "" || values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST"] != ""
	if !phaseBefore(state.Phase, phasePool) || hasPoolEnvironment {
		if values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"] != state.Authority.PoolID ||
			values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST"] != state.Authority.CompatibilityDigest ||
			state.Authority.PoolID == "" || state.Authority.CompatibilityDigest == "" {
			return errors.New("retained local runtime physical-pool environment disagrees with durable state")
		}
		allowed += 2
	}
	if len(values) != allowed {
		return errors.New("retained local runtime environment contains unowned fields; refusing mutation")
	}
	return nil
}

func slicesSort(values []string) {
	// Kept local so the controller remains buildable on every supported Go
	// toolchain used by the release workflow.
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
