package accesspostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesspg "github.com/flidai/leapview/internal/access/postgres"
	deploymentpg "github.com/flidai/leapview/internal/deployment/postgres"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatepg "github.com/flidai/leapview/internal/servingstate/postgres"
)

// InitializeActiveTargetAuthorizationPolicy upgrades a target that predates
// target-owned policies from its exact active serving generation. It holds the
// target share lock through commit so activation cannot change the observed
// generation while its immutable policy is imported.
func InitializeActiveTargetAuthorizationPolicy(
	ctx context.Context,
	begin func(context.Context) (accesspg.Tx, error),
	targets *deploymentpg.Repository,
	states *servingstatepg.Repository,
	policies *accesspg.Repository,
	targetID, environment string,
) error {
	if begin == nil || targets == nil || states == nil || policies == nil || strings.TrimSpace(targetID) == "" || strings.TrimSpace(environment) == "" {
		return errors.New("target authorization policy upgrade dependencies are unavailable")
	}
	tx, err := begin(ctx)
	if err != nil {
		return fmt.Errorf("begin target authorization policy upgrade: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Activation takes this same target row FOR UPDATE. Holding its share lock
	// until commit binds the imported serving-policy document and its source
	// generation to one stable active-pointer observation.
	target, err := targets.TargetForShareTx(ctx, tx, targetID)
	if errors.Is(err, deploymentpg.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read target for authorization policy upgrade: %w", err)
	}
	if target.TargetID != targetID || target.Environment != environment || strings.TrimSpace(target.ProjectID) == "" {
		return errors.New("existing delivery target does not match authorization policy upgrade scope")
	}
	if target.ActiveGenerationID == "" {
		return nil
	}
	scope := access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: target.ProjectID, Environment: target.Environment}
	if _, err := accesspg.AuthorizationPolicyTx(ctx, tx, scope); err == nil {
		return nil
	} else if !errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
		return fmt.Errorf("read target authorization policy before upgrade: %w", err)
	}
	state, err := states.WithTx(tx).ByID(ctx, servingstate.ID(target.ActiveGenerationID))
	if err != nil {
		return fmt.Errorf("read active serving policy for authorization upgrade: %w", err)
	}
	if string(state.ID) != target.ActiveGenerationID || state.ProjectID.String() != target.ProjectID || string(state.Environment) != target.Environment || state.Status != servingstate.StatusActive {
		return errors.New("active serving policy does not match authorization policy upgrade target")
	}
	bindings, err := projectmodule.DecodeAuthorizationRoleBindingsJSON(state.AccessPolicyJSON)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		// Native generations created before target-owned policy governance carry
		// the canonical empty document. Do not turn that absence into revision one:
		// bootstrap-project must still be able to establish the claiming principal
		// as the first administrator. The bootstrap authorizer keeps this exact
		// legacy state narrowly open until a policy-bearing generation is active.
		return nil
	}
	if _, err := accesspg.InitializeAuthorizationPolicyFromServingPolicyTx(ctx, tx, scope, target.ActiveGenerationID, bindings); err != nil {
		return fmt.Errorf("initialize target authorization policy from active generation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit target authorization policy upgrade: %w", err)
	}
	return nil
}
