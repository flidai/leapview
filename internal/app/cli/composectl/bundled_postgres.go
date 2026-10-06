package composectl

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const (
	bundledPostgresProfileFile = ".host-postgres-profile"
	bundledPostgresProfile     = "bundled-postgres\n"
	externalPostgresProfile    = "external-postgres\n"
	bundledPostgresSecretDir   = ".postgres-secrets"
	bundledPostgresCADir       = "/run/leapview/postgres"
	bundledPostgresCAPath      = bundledPostgresCADir + "/ca.crt"
	bundledPostgresServerName  = "postgres"
	bundledPostgresReadyFile   = ".postgres-credentials-ready"
	bundledPostgresReady       = "bundled-postgres-credentials-v1\n"
)

var bundledPostgresRoleSecrets = []string{
	"bootstrap-password",
	"control-runtime-password",
	"control-migrator-password",
	"control-maintenance-password",
	"ducklake-runtime-password",
	"ducklake-migrator-password",
	"ducklake-maintenance-password",
}

func bundledPostgresSelected(root string) (bool, error) {
	profile, err := installedPostgresProfile(root)
	if err != nil {
		return false, err
	}
	return profile == FirstInstallPostgresBundled, nil
}

func installedPostgresProfile(root string) (string, error) {
	path := filepath.Join(root, bundledPostgresProfileFile)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		remains, secretErr := bundledPostgresCredentialStateRemains(root)
		if secretErr != nil {
			return "", secretErr
		}
		if remains {
			return "", errors.New("PostgreSQL profile marker is missing but bundled credentials remain")
		}
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect PostgreSQL profile selection: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64 {
		return "", errors.New("PostgreSQL profile selection must be a small private regular file")
	}
	contents, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return "", fmt.Errorf("read PostgreSQL profile selection: %w", err)
	}
	switch string(contents) {
	case externalPostgresProfile:
		remains, secretErr := bundledPostgresCredentialStateRemains(root)
		if secretErr != nil {
			return "", secretErr
		}
		if remains {
			return "", errors.New("external PostgreSQL profile conflicts with bundled credentials")
		}
		return FirstInstallPostgresExternal, nil
	case bundledPostgresProfile:
		return FirstInstallPostgresBundled, nil
	default:
		return "", errors.New("PostgreSQL profile selection is invalid")
	}
}

func bundledPostgresCredentialStateRemains(root string) (bool, error) {
	for _, name := range []string{bundledPostgresSecretDir, bundledPostgresReadyFile} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect bundled PostgreSQL credentials: %w", err)
		}
	}
	return false, nil
}

func selectBundledPostgres(root string) error {
	return selectPostgresProfile(root, FirstInstallPostgresBundled)
}

func selectPostgresProfile(root, profile string) error {
	var contents []byte
	switch profile {
	case FirstInstallPostgresExternal:
		contents = []byte(externalPostgresProfile)
	case FirstInstallPostgresBundled:
		contents = []byte(bundledPostgresProfile)
	default:
		return fmt.Errorf("unsupported PostgreSQL profile %q", profile)
	}
	if err := securefs.EnsurePrivateDir(root); err != nil {
		return err
	}
	path := filepath.Join(root, bundledPostgresProfileFile)
	selected, err := installedPostgresProfile(root)
	if err != nil {
		return err
	}
	if selected != "" && selected != profile {
		return fmt.Errorf("installed PostgreSQL profile is %s and cannot be changed to %s", selected, profile)
	}
	err = securefs.WritePrivateFileAtomicOnce(path, contents, securefs.PrivateFileMode)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("persist PostgreSQL profile selection: %w", err)
	}
	if os.IsExist(err) {
		selected, selectErr := installedPostgresProfile(root)
		if selectErr != nil {
			return selectErr
		}
		if selected != profile {
			return fmt.Errorf("installed PostgreSQL profile is %s and cannot be changed to %s", selected, profile)
		}
		return nil
	}
	return nil
}

func rejectBundledPostgresSelection(root string) error {
	return selectPostgresProfile(root, FirstInstallPostgresExternal)
}

func (c *Controller) ensureBundledPostgres(ctx context.Context) (FirstInstallPostgres, error) {
	if c == nil {
		return FirstInstallPostgres{}, errors.New("controller is required")
	}
	if err := selectPostgresProfile(c.root, FirstInstallPostgresBundled); err != nil {
		return FirstInstallPostgres{}, err
	}
	secretRoot := filepath.Join(c.root, bundledPostgresSecretDir)
	if err := ensureBundledPostgresSecrets(secretRoot); err != nil {
		return FirstInstallPostgres{}, fmt.Errorf("prepare persistent PostgreSQL credentials and TLS: %w", err)
	}
	if err := c.compose(ctx, nil, c.stdout, c.stderr, "up", "-d", "--wait", "--wait-timeout", "120", "postgres"); err != nil {
		return FirstInstallPostgres{}, fmt.Errorf("start bundled PostgreSQL: %w", err)
	}
	// The image's docker-entrypoint-initdb.d hook handles a new volume. Running
	// the same idempotent SQL on every prepare/init/apply attempt also resumes a
	// volume where initdb completed but role or database setup was interrupted.
	const retryableBootstrap = "LEAPVIEW_POSTGRES_BOOTSTRAP_HOST=postgres exec /bin/sh /run/leapview-postgres/bundled-init.sh"
	if err := c.compose(ctx, nil, c.stdout, c.stderr,
		"exec", "-T", "postgres", "/bin/sh", "-ec", retryableBootstrap,
	); err != nil {
		return FirstInstallPostgres{}, fmt.Errorf("reconcile bundled PostgreSQL roles, databases, and schema: %w", err)
	}
	return bundledPostgresConnections(secretRoot)
}

func ensureBundledPostgresSecrets(secretRoot string) error {
	if info, err := os.Lstat(secretRoot); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("persistent PostgreSQL credential path must be a real directory")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect persistent PostgreSQL credential directory: %w", err)
	}
	if err := securefs.EnsurePrivateDir(secretRoot); err != nil {
		return err
	}
	readyPath := filepath.Join(filepath.Dir(secretRoot), bundledPostgresReadyFile)
	ready, err := readPostgresMaterial(readyPath, securefs.PrivateFileMode)
	complete := err == nil
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("validate bundled PostgreSQL credential-set marker: %w", err)
	}
	if complete && string(ready) != bundledPostgresReady {
		return errors.New("bundled PostgreSQL credential-set marker is invalid")
	}
	if complete {
		for _, name := range bundledPostgresRoleSecrets {
			if _, err := readPrivatePostgresSecret(filepath.Join(secretRoot, name)); err != nil {
				return fmt.Errorf("completed PostgreSQL credential set is missing or invalid: %s: %w", name, err)
			}
		}
		for name, mode := range map[string]os.FileMode{
			"ca.key": 0o600, "ca.crt": 0o644, "server.key": 0o600, "server.crt": 0o644,
		} {
			if _, err := readPostgresMaterial(filepath.Join(secretRoot, name), mode); err != nil {
				return fmt.Errorf("completed PostgreSQL credential set is missing or invalid: %s: %w", name, err)
			}
		}
	}
	for _, name := range bundledPostgresRoleSecrets {
		path := filepath.Join(secretRoot, name)
		if _, err := readPrivatePostgresSecret(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("validate persistent PostgreSQL secret %s: %w", name, err)
		}
		var entropy [32]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return fmt.Errorf("generate PostgreSQL secret %s: %w", name, err)
		}
		if _, err := createPostgresFileOnce(path, []byte(base64.RawURLEncoding.EncodeToString(entropy[:])), securefs.PrivateFileMode); err != nil {
			return fmt.Errorf("persist PostgreSQL secret %s: %w", name, err)
		}
	}
	if err := ensureBundledPostgresCertificates(secretRoot); err != nil {
		return err
	}
	if !complete {
		marker, err := createPostgresFileOnce(readyPath, []byte(bundledPostgresReady), securefs.PrivateFileMode)
		if err != nil {
			return fmt.Errorf("publish complete PostgreSQL credential set: %w", err)
		}
		if string(marker) != bundledPostgresReady {
			return errors.New("bundled PostgreSQL credential-set marker is invalid")
		}
	}
	return nil
}

func createPostgresFileOnce(path string, contents []byte, mode os.FileMode) ([]byte, error) {
	err := securefs.WritePrivateFileAtomicOnce(path, contents, mode)
	if err != nil {
		if os.IsExist(err) {
			return readPostgresMaterial(path, mode)
		}
		return nil, err
	}
	return append([]byte(nil), contents...), nil
}

func readPrivatePostgresSecret(path string) ([]byte, error) {
	contents, err := readPostgresMaterial(path, securefs.PrivateFileMode)
	if err != nil {
		return nil, err
	}
	if len(contents) != 43 || strings.ContainsAny(string(contents), "\x00\r\n") {
		return nil, errors.New("PostgreSQL secret is malformed")
	}
	if _, err := base64.RawURLEncoding.DecodeString(string(contents)); err != nil {
		return nil, errors.New("PostgreSQL secret is malformed")
	}
	return contents, nil
}

func readPostgresMaterial(path string, mode os.FileMode) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return nil, errors.New("PostgreSQL material must be a regular file with its required permissions")
	}
	return os.ReadFile(path)
}

func bundledPostgresConnections(secretRoot string) (FirstInstallPostgres, error) {
	readURL := func(role, database, secretName string) (string, error) {
		password, err := readPrivatePostgresSecret(filepath.Join(secretRoot, secretName))
		if err != nil {
			return "", err
		}
		connection := &url.URL{
			Scheme: "postgres", Host: bundledPostgresServerName, Path: "/" + database,
			RawQuery: url.Values{"sslmode": {"verify-full"}, "sslrootcert": {bundledPostgresCAPath}}.Encode(),
		}
		connection.User = url.UserPassword(role, string(password))
		return connection.String(), nil
	}
	var postgres FirstInstallPostgres
	var err error
	if postgres.ControlURL, err = readURL(postgresControlRuntimeRole, postgresControlDatabase, "control-runtime-password"); err != nil {
		return FirstInstallPostgres{}, err
	}
	if postgres.ControlMigratorURL, err = readURL(postgresControlMigratorRole, postgresControlDatabase, "control-migrator-password"); err != nil {
		return FirstInstallPostgres{}, err
	}
	if postgres.ControlMaintenanceURL, err = readURL(postgresControlMaintenanceRole, postgresControlDatabase, "control-maintenance-password"); err != nil {
		return FirstInstallPostgres{}, err
	}
	if postgres.DuckLakeURL, err = readURL(postgresDuckLakeRuntimeRole, postgresDuckLakeDatabase, "ducklake-runtime-password"); err != nil {
		return FirstInstallPostgres{}, err
	}
	if postgres.DuckLakeMaintenanceURL, err = readURL(postgresDuckLakeMaintenanceRole, postgresDuckLakeDatabase, "ducklake-maintenance-password"); err != nil {
		return FirstInstallPostgres{}, err
	}
	if postgres.DuckLakeMigratorURL, err = readURL(postgresDuckLakeMigratorRole, postgresDuckLakeDatabase, "ducklake-migrator-password"); err != nil {
		return FirstInstallPostgres{}, err
	}
	return postgres, nil
}
