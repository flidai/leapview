package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
)

// candidateProvenanceTransactionReader reads immutable release provenance for
// ordinary publication's credential pin continuity check.
type candidateProvenanceTransactionReader interface {
	Configured() bool
	CandidateProvenanceTx(context.Context, releasepostgres.DBTX, projectgraph.ResourceID, string, int64) (release.Provenance, error)
}

func newOrdinaryCredentialPublicationAdmission(delivery *deploymentpostgres.Repository, provenance candidateProvenanceTransactionReader) (deploymentpostgres.ActivationAdmissionPort, error) {
	if delivery == nil || !delivery.Configured() || typednil.IsNil(provenance) || !provenance.Configured() {
		return nil, errors.New("ordinary credential publication admission requires configured delivery and provenance authorities")
	}
	return func(ctx context.Context, tx deploymentpostgres.Tx, publication deploymentpostgres.DeliveryPublication) error {
		if err := admitPublicationWithoutCredentialOperation(ctx, tx, publication); err != nil {
			return err
		}
		return verifyPublicationCredentialContinuity(ctx, tx, delivery, provenance, publication)
	}, nil
}

// The delivery target is the instance identity used by source credential
// preparations. This check is installed even when customer-key setup is absent:
// a setup change cannot make an existing operation invisible to publication.
// Delivery holds the target lock before this fresh statement, and preparation
// and cancellation must take that same lock before writing their records.
func admitPublicationWithoutCredentialOperation(ctx context.Context, tx deploymentpostgres.Tx, publication deploymentpostgres.DeliveryPublication) error {
	if err := credentialpostgres.CheckNoPendingActivationTx(ctx, tx, publication.TargetID); err != nil {
		if errors.Is(err, credential.ErrConflict) {
			return fmt.Errorf("%w: a credential activation is unfinished", deploymentpostgres.ErrConflict)
		}
		return fmt.Errorf("credential publication admission is unavailable: %w", err)
	}
	return nil
}
