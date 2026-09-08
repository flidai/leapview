package postgres

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/recoveryset/observation"
)

// CapturedObservation is opaque trusted evidence produced by Capture.
// Callers cannot construct a value containing arbitrary marker or inventory
// data because the underlying fields are private to this package.
type CapturedObservation struct {
	boundary  observation.Boundary
	inventory observation.Inventory
}

// Boundary returns a value copy of the trusted PostgreSQL boundary.
func (c CapturedObservation) Boundary() observation.Boundary { return c.boundary }

// Inventory returns a defensive deep copy of the trusted managed-data
// inventory. The returned slices may be changed by the caller safely.
func (c CapturedObservation) Inventory() observation.Inventory {
	out := c.inventory
	out.Revisions = make([]observation.Revision, len(c.inventory.Revisions))
	copy(out.Revisions, c.inventory.Revisions)
	for i := range out.Revisions {
		out.Revisions[i].Files = make([]observation.File, len(c.inventory.Revisions[i].Files))
		copy(out.Revisions[i].Files, c.inventory.Revisions[i].Files)
	}
	return out
}

// Capture converts trusted source facts into the recovery-set observation
// contract. The source generates the PostgreSQL marker and inventory facts;
// callers provide neither marker, identity, nor inventory data.
func Capture(ctx context.Context, source manageddata.ProjectionCaptureSource) (CapturedObservation, error) {
	if ctx == nil || source == nil {
		return CapturedObservation{}, fmt.Errorf("%w: provider observation source is required", observation.ErrInvalid)
	}
	projection, err := source.CaptureManagedProjection(ctx)
	if err != nil {
		// Preserve the source error identity: database privilege, primary-role,
		// timeout, and transport failures are not observation-contract input
		// errors and must remain distinguishable to the caller.
		return CapturedObservation{}, fmt.Errorf("trusted managed-data capture: %w", err)
	}
	inventory := observation.Inventory{
		SchemaVersion: observation.SchemaVersion,
		Revisions:     make([]observation.Revision, 0, len(projection.Revisions)),
	}
	for _, revision := range projection.Revisions {
		files := make([]observation.File, 0, len(revision.Files))
		for _, file := range revision.Files {
			files = append(files, observation.File{
				Path:       file.Path,
				SHA256:     file.SHA256,
				StorageKey: file.StorageKey,
				Size:       file.Size,
			})
		}
		inventory.Revisions = append(inventory.Revisions, observation.Revision{
			RevisionID:     revision.RevisionID,
			ManifestDigest: revision.ManifestDigest,
			Files:          files,
		})
	}
	inventoryDigest, err := inventory.Digest()
	if err != nil {
		return CapturedObservation{}, fmt.Errorf("%w: digest trusted managed-data inventory: %v", observation.ErrInvalid, err)
	}
	boundary := observation.Boundary{
		ProtocolVersion:  observation.ProtocolVersion,
		DatabaseIdentity: projection.DatabaseIdentity,
		SystemIdentity:   projection.SystemIdentity,
		Timeline:         projection.Timeline,
		LSN:              projection.LSN,
		RestorePointName: projection.RestorePointName,
		InventoryDigest:  inventoryDigest,
	}
	if err := boundary.Validate(); err != nil {
		return CapturedObservation{}, fmt.Errorf("%w: validate captured PostgreSQL boundary: %v", observation.ErrInvalid, err)
	}
	if err := inventory.Validate(); err != nil {
		return CapturedObservation{}, fmt.Errorf("%w: validate captured managed-data inventory: %v", observation.ErrInvalid, err)
	}
	return CapturedObservation{boundary: boundary, inventory: inventory}, nil
}
