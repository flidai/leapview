// Package module exposes credential composition operations.
package module

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/credential/encryption"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
)

// CustomerOwnerReader supplies the persisted setup declaration.
type CustomerOwnerReader interface {
	CustomerOwner(context.Context) (string, error)
}

// CheckSetup keeps credential setup optional until selected, but partial setup must never
// silently fall back to deployment secrets or a different encryption key.
func CheckSetup(ctx context.Context, reader CustomerOwnerReader, instanceID, keyringPath string) error {
	_, _, err := configuredKeyring(ctx, reader, instanceID, keyringPath)
	return err
}

// configuredKeyring distinguishes a deliberately unconfigured installation
// from a partially configured or mismatched credential installation.
func configuredKeyring(ctx context.Context, reader CustomerOwnerReader, instanceID, keyringPath string) (*encryption.Keyring, bool, error) {
	if ctx == nil || reader == nil {
		return nil, false, errors.New("customer credential setup reader and context are required")
	}
	owner, err := reader.CustomerOwner(ctx)
	if errors.Is(err, platformbootstrap.ErrNotFound) && keyringPath == "" {
		return nil, false, nil
	}
	if errors.Is(err, platformbootstrap.ErrNotFound) {
		return nil, false, errors.New("customer credential ownership is missing; stop the app and run admin credentials setup --owner with the declared customer owner")
	}
	if err != nil {
		return nil, false, fmt.Errorf("read customer credential ownership: %w", err)
	}
	if owner == "" || keyringPath == "" {
		return nil, false, errors.New("customer credential setup requires its declared owner and LEAPVIEW_CREDENTIAL_KEYRING_FILE")
	}
	keys, err := encryption.Load(keyringPath)
	if err != nil {
		return nil, false, fmt.Errorf("load customer credential keyring: %w", err)
	}
	if keys.DeploymentID() != instanceID {
		return nil, false, errors.New("customer credential keyring deployment does not match the durable instance identity")
	}
	return keys, true, nil
}
