package objectstore

import (
	"context"
	"fmt"
	"path/filepath"
)

// FilesystemBackupRelativePath maps one immutable logical key to its actual
// self-contained envelope file. It performs no filesystem effects.
func FilesystemBackupRelativePath(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return filepath.FromSlash(key) + filesystemObjectSuffix, nil
}

// VerifyFilesystemBackupFile verifies an already restored envelope with the
// same bounded key/body/domain parser used by FilesystemStore.Open. Backup
// transport must separately retain and verify the complete envelope bytes.
func VerifyFilesystemBackupFile(ctx context.Context, path, key string, expected ObjectMetadata) (ObjectInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return ObjectInfo{}, fmt.Errorf("%w: canonical envelope file required", ErrInvalid)
	}
	if err := validateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	store := &FilesystemStore{root: filepath.Dir(path), securityDomain: expected.StorageSecurityDomain, maxObjectBytes: MaxObjectBytes}
	if err := store.validateMetadata(expected); err != nil {
		return ObjectInfo{}, err
	}
	if err := validateFilesystemRoot(store.root); err != nil {
		return ObjectInfo{}, err
	}
	info, err := store.readAndVerify(ctx, path, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if info.StorageSecurityDomain != expected.StorageSecurityDomain || info.Digest != expected.Digest || info.SizeBytes != expected.SizeBytes || info.ContentType != expected.ContentType || info.MetadataDigest != expected.MetadataDigest {
		return ObjectInfo{}, fmt.Errorf("%w: retained envelope metadata differs", ErrConflict)
	}
	return info, nil
}

// ValidateFilesystemBackupMetadata validates retained metadata without opening
// or creating a store, using the same policy as the filesystem adapter.
func ValidateFilesystemBackupMetadata(metadata ObjectMetadata) error {
	return (&FilesystemStore{securityDomain: metadata.StorageSecurityDomain, maxObjectBytes: MaxObjectBytes}).validateMetadata(metadata)
}
