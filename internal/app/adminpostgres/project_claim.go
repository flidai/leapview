package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/deploymentaudit"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// BootstrapProjectClaim is the strict local image-side adapter for the
// existing transactional Project-claim and Access audit authorities. It is
// unavailable for production and never depends on an expiring bootstrap API
// token, so an interrupted local bootstrap remains recoverable.
func (o Operations) BootstrapProjectClaim(ctx context.Context, request admincli.ProjectClaimBootstrapRequest, out io.Writer) error {
	if out == nil {
		return errors.New("Project-claim output is required")
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Production {
		return errors.New("native local Project-claim bootstrap is unavailable in production")
	}
	if err := validateLocalAdminConfiguration(cfg); err != nil {
		return fmt.Errorf("native local Project-claim bootstrap is unavailable: %w", err)
	}
	result, err := bootstrapNativeProjectClaim(ctx, cfg, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		InstanceID  string `json:"instanceId"`
		ProjectUID  string `json:"projectUid"`
		Environment string `json:"environment"`
		ClaimedBy   string `json:"claimedBy"`
		ClaimedAt   string `json:"claimedAt"`
	}{
		InstanceID: result.InstanceID, ProjectUID: result.ProjectUID, Environment: result.Environment,
		ClaimedBy: result.ClaimedBy, ClaimedAt: result.ClaimedAt.UTC().Format(time.RFC3339Nano),
	})
}

func bootstrapNativeProjectClaim(ctx context.Context, cfg config.Config, request admincli.ProjectClaimBootstrapRequest) (admincli.ProjectClaimBootstrapResult, error) {
	accessConfig, err := accessConfigForAdmin(cfg)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	pool, err := platformpostgres.OpenControl(ctx, accessConfig)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, fmt.Errorf("open local Project-claim authority: %w", err)
	}
	defer pool.Close()
	if err := postgresbaseline.VerifyProvider(ctx, pool); err != nil {
		return admincli.ProjectClaimBootstrapResult{}, fmt.Errorf("verify local Project-claim baseline: %w", err)
	}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	accessAuthority, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: key})
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	bootstrapEmail, err := adminoffline.ResolveBootstrapEmail(false, cfg.BootstrapEmail)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	principal, err := resolveLocalBootstrapAdministrator(ctx, accessAuthority, bootstrapEmail)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	principalID := principal.ID
	bootstrap := platformbootstrap.New(pool)
	instanceID, err := bootstrap.InstanceID(ctx)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, fmt.Errorf("resolve local instance identity: %w", err)
	}
	boundEnvironment, err := bootstrap.InstanceEnvironment(ctx)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, fmt.Errorf("resolve local instance environment: %w", err)
	}
	if boundEnvironment != strings.TrimSpace(request.Environment) || boundEnvironment != strings.TrimSpace(cfg.Environment) {
		return admincli.ProjectClaimBootstrapResult{}, errors.New("local Project-claim environment differs from the durable instance binding")
	}
	repository := deploymentpostgres.New(pool)
	persistence, err := deploymentmodule.NewPostgresPersistence(repository)
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	audit := deploymentaudit.NewWithRepository(accesspostgres.New())
	result, err := deploymentmodule.BootstrapProjectClaimNative(ctx, persistence, audit, instanceID, boundEnvironment, deploymentmodule.ProjectClaimBootstrapInput{
		PrincipalID: principalID, ProjectUID: request.ProjectUID, IssuerID: request.IssuerID,
		Environment: request.Environment, IdempotencyKey: request.OperationID,
	})
	if err != nil {
		return admincli.ProjectClaimBootstrapResult{}, err
	}
	if result.Conflict {
		return admincli.ProjectClaimBootstrapResult{}, errors.New("local instance is already claimed by a different Project or environment")
	}
	return admincli.ProjectClaimBootstrapResult{
		InstanceID: instanceID, ProjectUID: result.Claim.ProjectID.String(), Environment: string(result.Claim.Environment),
		ClaimedBy: result.Claim.ClaimedBy, ClaimedAt: result.Claim.ClaimedAt,
	}, nil
}

type localBootstrapAccessAuthority interface {
	PrincipalByEmail(context.Context, string) (access.Principal, error)
	IsPlatformAdmin(context.Context, string) (bool, error)
}

func resolveLocalBootstrapAdministrator(ctx context.Context, authority localBootstrapAccessAuthority, email string) (access.Principal, error) {
	principal, err := authority.PrincipalByEmail(ctx, email)
	if err != nil {
		return access.Principal{}, fmt.Errorf("resolve local bootstrap administrator: %w", err)
	}
	admin, err := authority.IsPlatformAdmin(ctx, principal.ID)
	if err != nil {
		return access.Principal{}, fmt.Errorf("verify local bootstrap administrator: %w", err)
	}
	if !admin {
		return access.Principal{}, errors.New("local bootstrap principal is not an active platform administrator")
	}
	return principal, nil
}
