package managedrecovery

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/recoveryset"
)

// VerifyManagedClosure binds the retained file-content manifest to the exact
// native seal. The seal's root digest identifies its path, not the bytes in
// that path; each data and delete object needs a separate retained content
// identity. Other retained files may exist because a backup can cover several
// retained snapshots. They never replace a missing file from this seal.
func VerifyManagedClosure(seal recoveryset.SnapshotSeal, closure ducklake.NativeSnapshotClosureEvidence, manifest FileManifest) error {
	if err := ducklake.VerifyNativeSnapshotClosureEvidence(closure); err != nil {
		return err
	}
	if !filepath.IsAbs(closure.ObjectRoot) || filepath.Clean(closure.ObjectRoot) != closure.ObjectRoot || closure.ObjectRoot == "/" || closure.CatalogID != seal.CatalogID || closure.SnapshotID != seal.DuckLakeSnapshotID || closure.RelationNamespace != seal.RelationNamespace || closure.ObjectRoot != seal.ObjectRoot || closure.ObjectRootDigest != seal.ObjectRootDigest || closure.RelationManifestDigest != seal.RelationManifestDigest || closure.ClosureDigest != seal.ClosureDigest {
		return errors.New("managed file closure differs from the selected native seal")
	}
	if _, err := manifest.Digest(); err != nil {
		return err
	}
	files := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		files[file.Path] = true
	}
	for _, object := range closure.Objects {
		name, err := filepath.Rel(closure.ObjectRoot, object.Path)
		if err != nil || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || !files[filepath.ToSlash(name)] {
			return errors.New("managed content manifest omits a sealed DuckLake object")
		}
	}
	return nil
}
