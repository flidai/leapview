package deployment

import "time"

// These DTOs are the deployment/use-case boundary for durable physical-pool
// fencing and root enumeration. Storage adapters may alias them, but the
// deployment package must not depend on a particular database.
type GCFence struct {
	ID             string
	PhysicalPoolID string
	HolderID       string
	Epoch          int64
	RootRevision   int64
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type GCFenceRequest struct {
	ID             string
	PhysicalPoolID string
	HolderID       string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type DeliveryRoot struct {
	PhysicalPoolID string
	Kind           string
	SourceID       string
	CandidateID    string
	GenerationID   string
	LeaseID        string
	CatalogDigest  string
	ObjectKey      string
	Status         string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type RootSet struct {
	PhysicalPoolID string
	Revision       int64
	Roots          []DeliveryRoot
}
