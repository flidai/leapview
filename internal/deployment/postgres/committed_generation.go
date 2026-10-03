package postgres

import (
	"context"
	"errors"
	"fmt"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// CommittedGenerationEvidence proves that one immutable generation was
// durably committed by a publication for the exact target and serving
// identity. It does not prove that the generation is still active, ready for
// use, or retained for future activation.
type CommittedGenerationEvidence struct {
	TargetID              string
	Identity              projectgraph.ServingIdentity
	PublicationID         string
	CandidateID           string
	CandidateRevision     int64
	SnapshotSealID        string
	ServingArtifactDigest string
	BindingFingerprint    string
}

// CommittedGeneration returns the newest coherent committed publication for
// this exact target and serving identity. Historical committed generations
// remain eligible after a later publication changes the active pointer.
func (r *Repository) CommittedGeneration(ctx context.Context, targetID string, identity projectgraph.ServingIdentity) (CommittedGenerationEvidence, error) {
	db, err := requireDB(r)
	if err != nil {
		return CommittedGenerationEvidence{}, err
	}
	return committedGeneration(ctx, db, targetID, identity)
}

// CommittedGenerationTx reads commit evidence through the caller's transaction.
// Publication admission uses it while holding the target lock so the predecessor
// proof and the pointer comparison belong to the same transaction.
func (r *Repository) CommittedGenerationTx(ctx context.Context, tx Tx, targetID string, identity projectgraph.ServingIdentity) (CommittedGenerationEvidence, error) {
	if tx == nil {
		return CommittedGenerationEvidence{}, ErrInvalid
	}
	return committedGeneration(ctx, tx, targetID, identity)
}

func committedGeneration(ctx context.Context, db DBTX, targetID string, identity projectgraph.ServingIdentity) (CommittedGenerationEvidence, error) {
	var err error
	targetID, err = textID(targetID, "target id")
	if err != nil {
		return CommittedGenerationEvidence{}, err
	}
	if err := identity.Validate(); err != nil {
		return CommittedGenerationEvidence{}, fmt.Errorf("%w: serving identity: %v", ErrInvalid, err)
	}
	generationID, err := uuidID(identity.GenerationID, "generation id", false)
	if err != nil || generationID != identity.GenerationID {
		return CommittedGenerationEvidence{}, ErrInvalid
	}

	row, err := depdb.New(db).GetCommittedGenerationEvidence(ctx, depdb.GetCommittedGenerationEvidenceParams{
		TargetID: targetID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		GenerationID: dbUUID(generationID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CommittedGenerationEvidence{}, ErrNotFound
	}
	if err != nil {
		return CommittedGenerationEvidence{}, err
	}
	if row.TargetID != targetID || row.ProjectID != identity.ProjectID.String() || row.Environment != identity.Environment || row.GenerationID != generationID {
		return CommittedGenerationEvidence{}, fmt.Errorf("%w: committed generation proof identity differs", ErrConflict)
	}
	return CommittedGenerationEvidence{
		TargetID: row.TargetID, Identity: identity, PublicationID: row.PublicationID,
		CandidateID: row.CandidateID, CandidateRevision: row.CandidateRevision,
		SnapshotSealID: row.SnapshotSealID, ServingArtifactDigest: row.ServingArtifactDigest,
		BindingFingerprint: row.BindingFingerprint,
	}, nil
}
