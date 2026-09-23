package presentation

import "time"

// RunPublicationEvidence is durable confirmation that one root pipeline run
// committed a canonical refresh publication. Run completion alone is not
// publication evidence.
type RunPublicationEvidence struct {
	CommittedAt        time.Time
	SnapshotID         int64
	ResultGenerationID string
}
