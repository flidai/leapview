package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func (r *Repository) ArmBootstrapActivation(ctx context.Context, policy deployment.BootstrapActivationPolicy) (deployment.BootstrapActivationPolicy, error) {
	if err := policy.Validate(); err != nil {
		return deployment.BootstrapActivationPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.BootstrapActivationPolicy{}, err
	}
	defer tx.Rollback()
	result, err := deploydb.New(tx).InsertBootstrapActivationPolicy(ctx, deploydb.InsertBootstrapActivationPolicyParams{
		DeploymentID: policy.DeploymentID, ProjectID: policy.ProjectID.String(), Environment: string(policy.Environment), RequestDigest: policy.RequestDigest,
		ActorID: policy.ActorID, CredentialID: policy.CredentialID, CredentialExpiresAt: policy.CredentialExpiresAt.UTC().Format(time.RFC3339Nano), ArmedAt: policy.ArmedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		if existing, readErr := bootstrapPolicyByScopeTx(ctx, deploydb.New(tx), policy.ProjectID.String(), string(policy.Environment)); readErr == nil && existing.DeploymentID != policy.DeploymentID {
			return deployment.BootstrapActivationPolicy{}, deployment.ErrBootstrapPolicyConflict
		}
		return deployment.BootstrapActivationPolicy{}, mapError(err)
	}
	if inserted, _ := result.RowsAffected(); inserted != 1 {
		existing, readErr := bootstrapPolicyTx(ctx, deploydb.New(tx), policy.DeploymentID)
		if readErr != nil {
			return deployment.BootstrapActivationPolicy{}, readErr
		}
		if !sameBootstrapPolicyBinding(existing, policy) {
			return deployment.BootstrapActivationPolicy{}, deployment.ErrBootstrapPolicyConflict
		}
		return existing, nil
	}
	if err := tx.Commit(); err != nil {
		return deployment.BootstrapActivationPolicy{}, err
	}
	return policy, nil
}

func sameBootstrapPolicyBinding(first, second deployment.BootstrapActivationPolicy) bool {
	return first.ProjectID == second.ProjectID && first.Environment == second.Environment && first.DeploymentID == second.DeploymentID && first.RequestDigest == second.RequestDigest && first.ActorID == second.ActorID && first.CredentialID == second.CredentialID
}

func (r *Repository) BootstrapActivationPolicy(ctx context.Context, deploymentID string) (deployment.BootstrapActivationPolicy, error) {
	if deploymentID == "" || deploymentID != strings.TrimSpace(deploymentID) {
		return deployment.BootstrapActivationPolicy{}, deployment.ErrBootstrapPolicyNotFound
	}
	return bootstrapPolicyTx(ctx, deploydb.New(r.db), deploymentID)
}

type bootstrapPolicyQuerier interface {
	GetBootstrapActivationPolicy(context.Context, string) (deploydb.BootstrapActivationPolicy, error)
}

type bootstrapPolicyScopeQuerier interface {
	GetBootstrapActivationPolicyByScope(context.Context, deploydb.GetBootstrapActivationPolicyByScopeParams) (deploydb.BootstrapActivationPolicy, error)
}

func bootstrapPolicyTx(ctx context.Context, q bootstrapPolicyQuerier, id string) (deployment.BootstrapActivationPolicy, error) {
	row, err := q.GetBootstrapActivationPolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.BootstrapActivationPolicy{}, deployment.ErrBootstrapPolicyNotFound
	}
	if err != nil {
		return deployment.BootstrapActivationPolicy{}, err
	}
	return bootstrapPolicyFromRow(row)
}

func bootstrapPolicyByScopeTx(ctx context.Context, q bootstrapPolicyScopeQuerier, projectID, environment string) (deployment.BootstrapActivationPolicy, error) {
	row, err := q.GetBootstrapActivationPolicyByScope(ctx, deploydb.GetBootstrapActivationPolicyByScopeParams{ProjectID: projectID, Environment: environment})
	if err != nil {
		return deployment.BootstrapActivationPolicy{}, err
	}
	return bootstrapPolicyFromRow(row)
}

func bootstrapPolicyFromRow(row deploydb.BootstrapActivationPolicy) (deployment.BootstrapActivationPolicy, error) {
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.BootstrapActivationPolicy{}, fmt.Errorf("stored bootstrap project: %w", err)
	}
	credentialExpiresAt, err := time.Parse(time.RFC3339Nano, row.CredentialExpiresAt)
	if err != nil {
		return deployment.BootstrapActivationPolicy{}, fmt.Errorf("stored bootstrap credential expiry: %w", err)
	}
	armedAt, err := time.Parse(time.RFC3339Nano, row.ArmedAt)
	if err != nil {
		return deployment.BootstrapActivationPolicy{}, fmt.Errorf("stored bootstrap armed time: %w", err)
	}
	policy := deployment.BootstrapActivationPolicy{ProjectID: projectID, Environment: servingstate.Environment(row.Environment), DeploymentID: row.DeploymentID, RequestDigest: row.RequestDigest, ActorID: row.ActorID, CredentialID: row.CredentialID, CredentialExpiresAt: credentialExpiresAt.UTC(), ArmedAt: armedAt.UTC()}
	if err := policy.Validate(); err != nil {
		return deployment.BootstrapActivationPolicy{}, err
	}
	return policy, nil
}

var _ deployment.BootstrapActivationPolicyRepository = (*Repository)(nil)
