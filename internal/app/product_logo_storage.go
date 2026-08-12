package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	adminmodule "github.com/flidai/leapview/internal/admin/module"
	appconfig "github.com/flidai/leapview/internal/app/config"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
)

func productLogoBlobStore(ctx context.Context, config appconfig.Config) (adminmodule.ProductBlobStore, error) {
	prefix := strings.Trim(strings.TrimSpace(config.ManagedDataS3Prefix), "/")
	if prefix == "" {
		prefix = "managed-data"
	}
	blobs, err := manageddatamodule.NewContentBlobStore(ctx, managedDataProductConfig(config), filepath.Join(config.HomeDir, "product-logos"), prefix+"-product-logos")
	if err != nil {
		return nil, err
	}
	return productLogoStore{blobs: blobs}, nil
}

type productLogoStore struct {
	blobs manageddatamodule.ContentBlobStore
}

func (s productLogoStore) Put(ctx context.Context, expected adminmodule.ProductBlob, body io.Reader) (adminmodule.ProductBlob, error) {
	stored, err := s.blobs.PutContent(ctx, manageddatamodule.ContentBlob{SHA256: expected.SHA256, Size: expected.Size}, body)
	return adminmodule.ProductBlob{SHA256: stored.SHA256, Size: stored.Size}, err
}

func (s productLogoStore) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	reader, err := s.blobs.OpenContent(ctx, digest)
	if errors.Is(err, manageddatamodule.ErrContentBlobNotFound) {
		return nil, adminmodule.ErrProductLogoNotFound
	}
	return reader, err
}
