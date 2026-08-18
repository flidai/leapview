package runtimefactory

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	"github.com/flidai/leapview/internal/deployment"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/runtimehost"
)

// sealedDeliveryRootResolver binds the delivery pointer to the persisted
// serving-state graph artifact and exact catalog seal. It never infers an
// artifact identity from the runtime input or canonical generation ID.
func NewSQLiteSealedRootResolver(db *sql.DB, targetID string, delivery *deploymentsqlite.Repository, pools *physicalpoolsqlite.Repository) SealedRootResolver {
	return func(ctx context.Context, input runtimehost.RuntimeInput) (SealedServingRoot, error) {
		if delivery == nil || db == nil || targetID == "" {
			return SealedServingRoot{}, fmt.Errorf("%w: durable delivery repository is unavailable", ErrSealedRootUnavailable)
		}
		// Candidate preparation is isolated from the active pointer. Resolve the
		// exact ready candidate requested by runtimehost and bind its owner-scoped
		// artifact/seal; the caller's live authorization runs before lease acquire.
		if input.Candidate != nil && input.Candidate.CandidateID != "" {
			candidate, err := delivery.DeliveryCandidateByID(ctx, input.Candidate.CandidateID)
			if err != nil {
				return SealedServingRoot{}, err
			}
			if candidate.Status != deployment.DeliveryCandidateReady || candidate.TargetID != targetID || candidate.ProjectID.String() != input.State.ProjectID.String() || candidate.Environment != string(input.State.Environment) || candidate.ServingArtifactID != input.Artifact.ID || candidate.ServingArtifactDigest != input.Artifact.Digest {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate is not bound to requested serving artifact", ErrSealedRootMismatch)
			}
			seal, err := delivery.DeliveryCatalogSealByID(ctx, candidate.SealID)
			if err != nil {
				return SealedServingRoot{}, err
			}
			if err := validateSealedCandidate(candidate, seal); err != nil {
				return SealedServingRoot{}, err
			}
			poolContract, err := loadPoolContract(ctx, pools, candidate.PhysicalPoolID, candidate.CompatibilityDigest)
			if err != nil {
				return SealedServingRoot{}, err
			}
			if candidate.ServingStateID == "" {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate serving-state identity is missing", ErrSealedRootUnavailable)
			}
			persistedStateID, err := persistedServingStateID(ctx, db, candidate.ServingArtifactID)
			if err != nil {
				return SealedServingRoot{}, err
			}
			if persistedStateID != candidate.ServingStateID || candidate.ServingStateID != string(input.State.ID) {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate persisted state %q does not match artifact %q or requested state %q", ErrSealedRootMismatch, candidate.ServingStateID, persistedStateID, input.State.ID)
			}
			return SealedServingRoot{CandidateID: candidate.ID, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.ObjectKey, CatalogObjectSize: seal.ObjectSize, ClosureDigest: seal.ClosureDigest, QualificationDigest: seal.QualificationDigest, PhysicalPoolID: seal.PhysicalPoolID, Compatibility: poolContract.Tuple, PoolContract: poolContract, ServingStateID: candidate.ServingStateID, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest}, nil
		}
		generation, err := delivery.DeliveryGenerationByServingStateID(ctx, targetID, input.State.ProjectID.String(), string(input.State.Environment), string(input.State.ID))
		if err != nil {
			return SealedServingRoot{}, fmt.Errorf("%w: serving-state delivery generation: %v", ErrSealedRootUnavailable, err)
		}
		candidate, err := delivery.DeliveryCandidateByID(ctx, generation.CandidateID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		seal, err := delivery.DeliveryCatalogSealByID(ctx, candidate.SealID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		if generation.ServingArtifactID == "" || generation.ServingArtifactDigest == "" || candidate.ServingArtifactID == "" || candidate.ServingArtifactDigest == "" || seal.ServingArtifactID == "" || seal.ServingArtifactDigest == "" {
			return SealedServingRoot{}, fmt.Errorf("%w: persisted serving-artifact identity is missing", ErrSealedRootUnavailable)
		}
		if generation.ServingStateID == "" || generation.CompatibilityDigest == "" || candidate.ServingStateID == "" || candidate.CompatibilityDigest == "" || candidate.Status != deployment.DeliveryCandidateReady || seal.Status != deployment.CatalogSealVerified || candidate.CatalogDigest != generation.CatalogDigest || candidate.CatalogObjectKey != generation.CatalogObjectKey || candidate.PhysicalPoolID != generation.PhysicalPoolID || candidate.CompatibilityDigest != generation.CompatibilityDigest || candidate.ServingStateID != generation.ServingStateID || candidate.ServingArtifactID != generation.ServingArtifactID || candidate.ServingArtifactDigest != generation.ServingArtifactDigest || seal.CompatibilityDigest != generation.CompatibilityDigest || seal.ServingArtifactID != generation.ServingArtifactID || seal.ServingArtifactDigest != generation.ServingArtifactDigest {
			return SealedServingRoot{}, fmt.Errorf("%w: candidate, generation, and seal are not one verified tuple", ErrSealedRootMismatch)
		}
		if pools == nil {
			return SealedServingRoot{}, fmt.Errorf("%w: physical-pool admission repository is unavailable", ErrSealedRootUnavailable)
		}
		poolContract, err := loadPoolContract(ctx, pools, generation.PhysicalPoolID, seal.CompatibilityDigest)
		if err != nil {
			return SealedServingRoot{}, err
		}
		persistedStateID, err := persistedServingStateID(ctx, db, generation.ServingArtifactID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		if persistedStateID != generation.ServingStateID || generation.ServingStateID != string(input.State.ID) {
			return SealedServingRoot{}, fmt.Errorf("%w: delivery generation state %q does not match artifact state %q or requested state %q", ErrSealedRootMismatch, generation.ServingStateID, persistedStateID, input.State.ID)
		}
		return SealedServingRoot{
			GenerationID: generation.ID, CandidateID: candidate.ID, SealID: seal.ID,
			CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.ObjectKey, CatalogObjectSize: seal.ObjectSize,
			ClosureDigest: seal.ClosureDigest, QualificationDigest: seal.QualificationDigest,
			PhysicalPoolID: seal.PhysicalPoolID, Compatibility: poolContract.Tuple, PoolContract: poolContract,
			ServingStateID: generation.ServingStateID, ServingArtifactID: generation.ServingArtifactID, ServingArtifactDigest: generation.ServingArtifactDigest,
		}, nil
	}
}

func persistedServingStateID(ctx context.Context, db *sql.DB, artifactID string) (string, error) {
	if db == nil || artifactID == "" {
		return "", fmt.Errorf("%w: serving artifact identity is unavailable", ErrSealedRootUnavailable)
	}
	var stateID string
	if err := db.QueryRowContext(ctx, `SELECT serving_state_id FROM serving_state_artifacts WHERE id = ?`, artifactID).Scan(&stateID); err != nil {
		return "", fmt.Errorf("%w: serving artifact %q is not durably bound to a serving state: %v", ErrSealedRootUnavailable, artifactID, err)
	}
	if stateID == "" {
		return "", fmt.Errorf("%w: serving artifact %q has empty serving state binding", ErrSealedRootUnavailable, artifactID)
	}
	return stateID, nil
}

func validateSealedCandidate(candidate deployment.DeliveryCandidate, seal deployment.CatalogSeal) error {
	if candidate.Status != deployment.DeliveryCandidateReady || candidate.ServingStateID == "" || seal.Status != deployment.CatalogSealVerified || candidate.CatalogDigest != seal.CatalogDigest || candidate.CatalogObjectKey != seal.ObjectKey || candidate.PhysicalPoolID != seal.PhysicalPoolID || candidate.CompatibilityDigest != seal.CompatibilityDigest || candidate.QualificationDigest != seal.QualificationDigest || candidate.ServingArtifactID != seal.ServingArtifactID || candidate.ServingArtifactDigest != seal.ServingArtifactDigest {
		return fmt.Errorf("%w: candidate and seal are not one verified tuple", ErrSealedRootMismatch)
	}
	return nil
}

func loadPoolContract(ctx context.Context, pools *physicalpoolsqlite.Repository, poolID string, compatibilityDigest ...string) (*ducklake.PoolContract, error) {
	if pools == nil {
		return nil, fmt.Errorf("%w: physical-pool admission repository is unavailable", ErrSealedRootUnavailable)
	}
	var admission physicalpoolsqlite.AdmissionContract
	var err error
	if len(compatibilityDigest) > 0 && compatibilityDigest[0] != "" {
		admission, err = pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(poolID), compatibilityDigest[0])
	} else {
		admission, err = pools.LoadAdmissionContract(ctx, poolID)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: physical-pool admission: %v", ErrSealedRootUnavailable, err)
	}
	return &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}, nil
}
