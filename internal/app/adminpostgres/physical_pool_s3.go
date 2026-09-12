package adminpostgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/extension"
)

// physicalPoolS3Config is the production target projection used by both
// admission and catalog-upgrade ownership paths. Pool encryption references
// remain opaque in the contract; only the target's resolved provider key is
// sent to the S3 adapter.
func physicalPoolS3Config(cfg config.Config, extensionAdmission extension.Admission) gcadapter.S3Config {
	return gcadapter.S3Config{
		Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
		SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
		Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle,
		ExtensionAdmission: extensionAdmission,
		ResolveEncryptionKey: func(_ context.Context, reference string) (string, error) {
			expected := strings.TrimSpace(cfg.ObjectStoreS3EncryptionKeyRef)
			providerKey := strings.TrimSpace(cfg.ObjectStoreS3EncryptionProviderKey)
			if expected == "" || reference != expected || providerKey == "" {
				return "", fmt.Errorf("physical-pool encryption reference %q is not configured", reference)
			}
			return providerKey, nil
		},
	}
}
