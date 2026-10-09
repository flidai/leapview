package module

import (
	"context"
	"github.com/flidai/leapview/internal/credential/encryption"
)

// LoadConfiguredKeyring supplies the checked installation keyring to explicit
// application composition adapters; partial setup never falls back to other keys.
func LoadConfiguredKeyring(ctx context.Context, owner CustomerOwnerReader, instanceID, path string) (*encryption.Keyring, bool, error) {
	return configuredKeyring(ctx, owner, instanceID, path)
}
