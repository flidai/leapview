package postgres

import (
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/google/uuid"
)

// NewDashboardID, NewDraftID and NewRevisionID are the native service generators. Opaque
// authoring identities are UUIDv7 so inserts sort by creation time and can be
// correlated directly with the platform event/audit authorities.
func NewDashboardID() (authoring.DashboardID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return authoring.DashboardID(id.String()), nil
}

func NewDraftID() (authoring.DraftID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return authoring.DraftID(id.String()), nil
}

func NewRevisionID() (authoring.RevisionID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return authoring.RevisionID(id.String()), nil
}
