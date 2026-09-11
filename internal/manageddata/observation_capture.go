package manageddata

import (
	"context"

	"github.com/flidai/leapview/internal/manageddata/storage"
)

// ProviderVersionObservation is the managed-data owner's immutable provider
// fact. The alias keeps recovery composition on the managed-data contract
// boundary instead of importing its storage adapter package directly.
type ProviderVersionObservation = storage.ProviderVersionObservation

// ValidateProviderVersionObservation preserves the single validation owner
// for provider facts exposed through this contract boundary.
func ValidateProviderVersionObservation(observation ProviderVersionObservation) error {
	return storage.ValidateProviderVersionObservation(observation)
}

// CapturedProjection is the trusted source-side result of a managed-data
// capture. It contains only facts obtained from the managed-data authority;
// recovery-set adapters are responsible for canonicalizing these facts into
// their own observation contract.
type CapturedProjection struct {
	DatabaseIdentity string
	SystemIdentity   string
	Timeline         uint32
	LSN              string
	RestorePointName string
	Revisions        []CapturedProjectionRevision
}

type CapturedProjectionRevision struct {
	ProjectID      string
	CollectionID   string
	RevisionID     string
	ManifestDigest string
	Files          []CapturedProjectionFile
}

type CapturedProjectionFile struct {
	Path       string
	SHA256     string
	StorageKey string
	Size       int64
}

// ProjectionCaptureSource is the narrow trusted composition port used by
// recovery qualification. Implementations must obtain all marker and source
// facts themselves; callers provide no marker, inventory, or identity data.
type ProjectionCaptureSource interface {
	CaptureManagedProjection(context.Context) (CapturedProjection, error)
}
