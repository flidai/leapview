package providerrestore

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/flidai/leapview/internal/recoveryset"
)

const ManagedLocalHandoffSchemaVersion = 3
const ManagedLocalHandoffKind = "leapview/managed-local-replacement-handoff"
const ManagedLocalProfile = "managed-local-v1"

// ManagedLocalRoot retains native path/artifact identity separately from a
// file-content manifest captured under the selected backup writer frontier.
type ManagedLocalRoot struct {
	Root                  recoveryset.ObjectRoot `json:"root"`
	StorageRoot           string                 `json:"storageRoot,omitempty"`
	Destination           string                 `json:"destination"`
	ContentManifestDigest string                 `json:"contentManifestDigest"`
}

type ManagedLocalHandoff struct {
	Profile      string             `json:"profile"`
	OccurrenceID string             `json:"occurrenceId"`
	Roots        []ManagedLocalRoot `json:"roots"`
}

func validateManagedLocalHandoff(handoff ReplacementHandoff, set recoveryset.RecoverySet, artifactIdentity string) error {
	if handoff.Kind != ManagedLocalHandoffKind || handoff.Status != HandoffAvailable || handoff.RecoverySetID != set.ID || handoff.FrontierDigest != set.FrontierDigest || handoff.TargetID != set.Delivery.TargetID || handoff.AvailableAt.IsZero() || handoff.ManagedLocal == nil {
		return fmt.Errorf("%w: managed-local handoff identity incomplete", ErrInconsistent)
	}
	local := handoff.ManagedLocal
	if local.Profile != ManagedLocalProfile || strings.TrimSpace(local.OccurrenceID) == "" || len(local.Roots) != len(set.ObjectRoots) || len(local.Roots) < 2 {
		return fmt.Errorf("%w: managed-local handoff requires every authoritative root", ErrInconsistent)
	}
	if handoff.Artifact.Image != artifactIdentity || ociref.ValidateImmutable(handoff.Artifact.Image) != nil || placeholderOCIIdentity(handoff.Artifact.Image) || !sourceRevisionPattern.MatchString(handoff.Artifact.SourceRevision) || strings.Trim(handoff.Artifact.SourceRevision, string(handoff.Artifact.SourceRevision[0])) == "" || strings.TrimSpace(handoff.Artifact.Version) == "" || handoff.Artifact.Distribution != "oci" || handoff.Artifact.Platform != "linux/amd64" {
		return fmt.Errorf("%w: managed-local artifact is not an exact runnable release", ErrInconsistent)
	}
	seen := map[string]bool{}
	for _, entry := range local.Roots {
		if entry.Root.Validate() != nil || !slices.Contains(set.ObjectRoots, entry.Root) || seen[entry.Root.Kind+"\x00"+entry.Root.URI] || !validLocalContentDigest(entry.ContentManifestDigest) {
			return fmt.Errorf("%w: managed-local root differs from authoritative frontier", ErrInconsistent)
		}
		seen[entry.Root.Kind+"\x00"+entry.Root.URI] = true
		location, err := ManagedLocalRootPath(entry.Root, entry.StorageRoot)
		if err != nil || location != entry.Destination || !validLocalSnapshot(entry.Root.VersionID) || entry.Root.ProviderRecoveryFrontier != "restic:"+entry.Root.VersionID {
			return fmt.Errorf("%w: managed-local location/snapshot mismatch", ErrInconsistent)
		}
	}
	roles := []string{}
	for _, endpoint := range handoff.Providers {
		if endpoint.Role != "control" && endpoint.Role != "ducklake" {
			return fmt.Errorf("%w: managed-local handoff rejects remote object provider", ErrInconsistent)
		}
		if err := validateProviderEndpoint(endpoint); err != nil {
			return err
		}
		if !slices.Contains(handoff.Secrets.Keys, endpoint.CredentialSecretKey) || endpoint.TLSRootCASecretKey != "postgres.root-ca" || !slices.Contains(handoff.Secrets.Keys, endpoint.TLSRootCASecretKey) {
			return fmt.Errorf("%w: managed-local PostgreSQL key reference missing", ErrInconsistent)
		}
		roles = append(roles, endpoint.Role)
	}
	slices.Sort(roles)
	if !slices.Equal(roles, []string{"control", "ducklake"}) || !slices.Contains(handoff.Secrets.Keys, "deployment.keyring") {
		return fmt.Errorf("%w: managed-local database/keyring references incomplete", ErrInconsistent)
	}
	return validateSecretBundleReference(handoff.Secrets)
}

// ManagedLocalRootPath resolves an exact native artifact locator only below its
// explicitly retained storage root. Absolute DuckLake paths preserve the native
// path identity and cannot acquire a different root through this mapping.
func ManagedLocalRootPath(root recoveryset.ObjectRoot, storageRoot string) (string, error) {
	if root.Validate() != nil {
		return "", fmt.Errorf("%w: invalid native local root", ErrInconsistent)
	}
	location := root.URI
	if strings.HasPrefix(location, "serving-artifacts/") {
		if !canonicalLocalPath(storageRoot) {
			return "", fmt.Errorf("%w: explicit trusted artifact storage root required", ErrInconsistent)
		}
		return filepath.Join(storageRoot, location), nil
	}
	if storageRoot != "" {
		return "", fmt.Errorf("%w: absolute root rejects relative storage mapping", ErrInconsistent)
	}
	if strings.HasPrefix(location, "file:") {
		parsed, err := url.Parse(location)
		if err != nil || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			return "", fmt.Errorf("%w: invalid managed-local root", ErrInconsistent)
		}
		location = parsed.Path
	}
	if !canonicalLocalPath(location) {
		return "", fmt.Errorf("%w: canonical absolute local root required", ErrInconsistent)
	}
	return location, nil
}

func canonicalLocalPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && value != "/" && !strings.ContainsAny(value, "\r\n\t\x00:")
}

func validLocalSnapshot(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validLocalContentDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validLocalSnapshot(strings.TrimPrefix(value, "sha256:"))
}

func validateManagedLocalResults(handoff ReplacementHandoff, databases []DatabaseResult, objects []ObjectResult) error {
	if handoff.ManagedLocal == nil || len(objects) != len(handoff.ManagedLocal.Roots) || len(databases) != 2 {
		return fmt.Errorf("%w: incomplete managed-local restored providers", ErrInconsistent)
	}
	for _, entry := range handoff.ManagedLocal.Roots {
		matches := 0
		for _, object := range objects {
			if object.Kind == entry.Root.Kind && object.URI == entry.Root.URI && object.RequiredVersionID == entry.Root.VersionID && object.ObservedVersionID == entry.Root.VersionID && object.Digest == entry.Root.Digest {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%w: managed-local restore root mismatch", ErrInconsistent)
		}
	}
	for _, endpoint := range handoff.Providers {
		matches := 0
		for _, database := range databases {
			if string(database.DatabaseRole) == endpoint.Role && database.DatabaseIdentity == endpoint.Database {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%w: managed-local restored database mismatch", ErrInconsistent)
		}
	}
	return nil
}
