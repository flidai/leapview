package bundle

import (
	"context"
	"fmt"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/servingstate"
)

// ArtifactObjectReader is the least-privilege capability required to read a
// native content-addressed serving bundle. It intentionally excludes writes,
// listing, and deletion from runtime and refresh consumers.
type ArtifactObjectReader interface {
	Open(context.Context, string) (platformobjectstore.Object, error)
}

// ServingArtifactLoader resolves both embedded filesystem artifacts and
// native provider-neutral object locators into the same compiled contract.
// Native locators are never interpreted as filesystem paths or URLs.
type ServingArtifactLoader struct {
	Objects ArtifactObjectReader
}

func (l ServingArtifactLoader) LoadCompiled(ctx context.Context, artifact servingstate.Artifact, workspace string) (CompiledProjectArtifact, error) {
	if artifact.Path != "" {
		if artifact.Locator != "" {
			return CompiledProjectArtifact{}, fmt.Errorf("serving artifact cannot have both a filesystem path and native locator")
		}
		if artifact.Path != strings.TrimSpace(artifact.Path) || strings.TrimSpace(workspace) == "" {
			return CompiledProjectArtifact{}, fmt.Errorf("serving artifact filesystem input is not canonical")
		}
		if err := ExtractArtifact(artifact.Path, workspace); err != nil {
			return CompiledProjectArtifact{}, err
		}
		compiled, _, err := LoadCompiledProjectArtifact(workspace)
		return compiled, err
	}
	if l.Objects == nil {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact reader is unavailable")
	}
	if artifact.Locator == "" || artifact.Locator != strings.TrimSpace(artifact.Locator) || artifact.Format != servingstate.ArtifactBundleFormat || artifact.ContentType != servingstate.ArtifactBundleContentType || artifact.SizeBytes < 1 || artifact.SizeBytes > servingstate.MaxArtifactBundleBytes || artifact.StorageSecurityDomain == "" || artifact.StorageSecurityDomain != strings.TrimSpace(artifact.StorageSecurityDomain) {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact identity is incomplete")
	}
	if err := platformdigest.ValidateSHA256Identity(artifact.Digest); err != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact digest: %w", err)
	}
	if err := platformdigest.ValidateSHA256Identity(artifact.MetadataDigest); err != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact metadata digest: %w", err)
	}
	wantLocator := "serving-artifacts/" + strings.TrimPrefix(artifact.Digest, "sha256:") + ".tar.gz"
	if artifact.Locator != wantLocator {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact locator is not digest-derived")
	}
	object, err := l.Objects.Open(ctx, artifact.Locator)
	if err != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("open native serving artifact %q: %w", artifact.Locator, err)
	}
	if object.Body == nil {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact %q returned an empty body", artifact.Locator)
	}
	info := object.Info
	if info.Key != artifact.Locator || info.Digest != artifact.Digest || info.SizeBytes != artifact.SizeBytes || info.StorageSecurityDomain != artifact.StorageSecurityDomain || info.ContentType != artifact.ContentType || info.MetadataDigest != artifact.MetadataDigest {
		_ = object.Body.Close()
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact object evidence differs from durable serving state")
	}
	validation, compiled, validateErr := ValidateArtifactReader(object.Body, artifact.SizeBytes)
	closeErr := object.Body.Close()
	if validateErr != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("validate native serving artifact: %w", validateErr)
	}
	if closeErr != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("close native serving artifact: %w", closeErr)
	}
	if validation.Digest != artifact.Digest || validation.ManifestJSON != artifact.ManifestJSON {
		return CompiledProjectArtifact{}, fmt.Errorf("native serving artifact content differs from durable serving state")
	}
	return compiled, nil
}
