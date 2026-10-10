package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flidai/leapview/internal/app/providerrestore"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/pkg/strictjson"
)

const maxManagedCredentialsBytes = 1 << 20

// VerifyManagedKeyring reuses the credential module's existing durable customer
// owner and instance-bound keyring contract. A valid private file alone never
// supplies recovery authority, and a replacement host must retain the exact
// admitted bytes rather than generate a new keyring.
func VerifyManagedKeyring(ctx context.Context, credentials ManagedCredentials, owner credentialmodule.CustomerOwnerReader) error {
	before, err := readBoundedManagedPrivateFile(credentials.KeyringPath, maxManagedCredentialsBytes)
	if err != nil || digestBytes(before) != credentials.KeyringDigest {
		return errors.New("retained managed keyring digest differs")
	}
	if err := credentialmodule.CheckSetup(ctx, owner, credentials.InstanceID, credentials.KeyringPath); err != nil {
		return errors.New("managed keyring differs from the durable customer owner or instance")
	}
	after, err := readBoundedManagedPrivateFile(credentials.KeyringPath, maxManagedCredentialsBytes)
	if err != nil || digestBytes(after) != credentials.KeyringDigest {
		return errors.New("retained managed keyring changed during admission")
	}
	return nil
}

// ManagedSecretStore uses immutable digest-addressed documents in an already
// provisioned private directory. It neither resolves ambient credentials nor
// shares the legacy remote bundle's schema or storage authority.
type ManagedSecretStore struct{ Root string }

func (store ManagedSecretStore) Save(ctx context.Context, handoff providerrestore.ReplacementHandoff, roles RuntimeRoles, credentials ManagedCredentials) (providerrestore.SecretBundleReference, error) {
	if err := ctx.Err(); err != nil {
		return providerrestore.SecretBundleReference{}, err
	}
	if err := credentials.ValidateForHandoff(handoff, roles); err != nil {
		return providerrestore.SecretBundleReference{}, err
	}
	if err := validatePrivateSecretRoot(store.Root); err != nil {
		return providerrestore.SecretBundleReference{}, err
	}
	encoded, err := json.Marshal(credentials)
	if err != nil || len(encoded) > maxManagedCredentialsBytes {
		return providerrestore.SecretBundleReference{}, errors.New("managed private credential document exceeds bound")
	}
	digest := strings.TrimPrefix(digestBytes(encoded), "sha256:")
	path := filepath.Join(store.Root, digest+".json")
	if existing, err := readBoundedManagedPrivateFile(path, maxManagedCredentialsBytes); err == nil {
		if string(existing) != string(encoded) {
			return providerrestore.SecretBundleReference{}, errors.New("managed credential digest path contains different content")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return providerrestore.SecretBundleReference{}, err
	} else if err := securefs.WritePrivateFileAtomicOnce(path, encoded, 0600); err != nil {
		return providerrestore.SecretBundleReference{}, err
	}
	return providerrestore.SecretBundleReference{Provider: "host-provisioned-root-file", URI: "leapview-secret://host-provisioned/recovery/" + digest, SHA256: digest, Version: "managed-local-v1", Keys: []string{"deployment.keyring", "postgres.control.url", "postgres.ducklake.url", "postgres.root-ca"}}, nil
}

func (store ManagedSecretStore) Load(ctx context.Context, reference providerrestore.SecretBundleReference) (ManagedCredentials, error) {
	if err := ctx.Err(); err != nil {
		return ManagedCredentials{}, err
	}
	keys := slices.Clone(reference.Keys)
	slices.Sort(keys)
	if reference.Provider != "host-provisioned-root-file" || reference.Version != "managed-local-v1" || !validContentDigest("sha256:"+reference.SHA256) || reference.URI != "leapview-secret://host-provisioned/recovery/"+reference.SHA256 || !slices.Equal(keys, []string{"deployment.keyring", "postgres.control.url", "postgres.ducklake.url", "postgres.root-ca"}) {
		return ManagedCredentials{}, errors.New("exact managed-local secret reference required")
	}
	if err := validatePrivateSecretRoot(store.Root); err != nil {
		return ManagedCredentials{}, err
	}
	value, err := readBoundedManagedPrivateFile(filepath.Join(store.Root, reference.SHA256+".json"), maxManagedCredentialsBytes)
	if err != nil {
		return ManagedCredentials{}, err
	}
	if digestBytes(value) != "sha256:"+reference.SHA256 {
		return ManagedCredentials{}, errors.New("managed private credential digest mismatch")
	}
	var credentials ManagedCredentials
	if err := strictjson.Decode(value, &credentials); err != nil {
		return ManagedCredentials{}, err
	}
	if credentials.SchemaVersion != 1 || credentials.Profile != providerrestore.ManagedLocalProfile {
		return ManagedCredentials{}, errors.New("foreign credential profile denied")
	}
	return credentials, nil
}

func validatePrivateSecretRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return errors.New("canonical private managed secret root required")
	}
	for part := root; part != "/"; part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err != nil {
			return err
		}
		if !info.IsDir() || (part == root && (info.Mode().Perm()&0077 != 0 || !managedFileOwned(info))) {
			return errors.New("managed secret root has a link, invalid type or non-private permissions")
		}
	}
	return nil
}

func readBoundedManagedPrivateFile(path string, limit int64) ([]byte, error) {
	if err := validatePrivateSecretRoot(filepath.Dir(path)); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || !managedFileOwned(before) || before.Size() <= 0 || before.Size() > limit {
		return nil, errors.New("bounded private regular managed credential file required")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		file.Close()
		return nil, errors.New("managed credential file changed while opening")
	}
	value, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if err := errors.Join(readErr, statErr, closeErr); err != nil {
		return nil, err
	}
	current, err := root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || !managedFileOwned(current) || !os.SameFile(before, current) || before.Size() != int64(len(value)) || after.Size() != before.Size() || !before.ModTime().Equal(after.ModTime()) || len(value) > int(limit) {
		return nil, errors.New("managed credential file changed during read")
	}
	return value, nil
}
