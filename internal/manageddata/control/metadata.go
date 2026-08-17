package control

import (
	"context"

	"github.com/flidai/leapview/internal/manageddata"
)

// RevisionMetadata carries the upload provenance associated with a public
// revision. Implementations must scope revision lookup to its collection.
type RevisionMetadata struct {
	Revision manageddata.Revision
	// PublicID is the content-addressed identifier exposed by the transport
	// contract. Revision.ID remains the operational database identity.
	PublicID        string
	UploadSessionID string
}

// MetadataRepository exposes managed-data collection and revision metadata to
// delivery adapters without coupling the use case to a transport package.
type MetadataRepository interface {
	CollectionByProjectConnection(context.Context, string, string) (manageddata.Collection, error)
	RevisionByID(context.Context, string, string) (RevisionMetadata, error)
	ListRevisions(context.Context, string) ([]RevisionMetadata, error)
	ListUploadSessions(context.Context, string) ([]manageddata.UploadSession, error)
	EnvironmentPointer(context.Context, string, manageddata.Environment) (manageddata.EnvironmentPointer, error)
}
