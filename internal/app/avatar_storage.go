package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	appconfig "github.com/flidai/leapview/internal/app/config"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
)

func profileImageBlobStore(ctx context.Context, config appconfig.Config) (accessmodule.AvatarBlobStore, error) {
	prefix := strings.Trim(strings.TrimSpace(config.ManagedDataS3Prefix), "/")
	if prefix == "" {
		prefix = "managed-data"
	}
	blobs, err := manageddatamodule.NewContentBlobStore(ctx, managedDataProductConfig(config), filepath.Join(config.HomeDir, "profile-images"), prefix+"-profile-images")
	if err != nil {
		return nil, fmt.Errorf("initialize profile-image storage: %w", err)
	}
	return profileImageStore{blobs: blobs}, nil
}

type profileImageStore struct {
	blobs manageddatamodule.ContentBlobStore
}

func (s profileImageStore) Put(ctx context.Context, expected accessmodule.AvatarBlob, body io.Reader) (accessmodule.AvatarBlob, error) {
	stored, err := s.blobs.PutContent(ctx, manageddatamodule.ContentBlob{SHA256: expected.SHA256, Size: expected.Size}, body)
	return accessmodule.AvatarBlob{SHA256: stored.SHA256, Size: stored.Size}, err
}

func (s profileImageStore) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	reader, err := s.blobs.OpenContent(ctx, digest)
	if errors.Is(err, manageddatamodule.ErrContentBlobNotFound) {
		return nil, accessmodule.ErrAvatarBlobNotFound
	}
	return reader, err
}
