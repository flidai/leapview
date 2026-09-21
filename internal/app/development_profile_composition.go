package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func buildDevelopmentProfileAPI(
	accessModule *accessmodule.Module,
	analyticsModule *analyticsmodule.Module,
	profileApplications connectionbinding.ProfileApplicationStore,
	resolveProjectID func(context.Context) (projectgraph.ResourceID, error),
	targetID string,
	runtimeConfig runtimeAssemblyInputs,
	administration analyticsmodule.ConnectionBindingAdministration,
) (analyticsmodule.DevelopmentProfileApplicationAPIConfig, error) {
	if runtimeConfig.Production || profileApplications == nil || runtimeConfig.LocalCheckoutID == "" || runtimeConfig.LocalRuntimeID == "" || runtimeConfig.DevelopmentProfileName == "" {
		return analyticsmodule.DevelopmentProfileApplicationAPIConfig{}, nil
	}
	resolver, err := analyticsModule.DevelopmentProfileCredentialResolver()
	if err != nil {
		return analyticsmodule.DevelopmentProfileApplicationAPIConfig{}, fmt.Errorf("build development profile credential resolver: %w", err)
	}
	profileService, err := connectionbinding.NewProfileApplicationService(connectionbinding.ProfileApplicationServiceConfig{
		Store: profileApplications, Bindings: administration, Resolver: resolver,
		NewBindingID: func() (connectionbinding.BindingID, error) {
			return connectionbinding.ParseBindingID("binding:" + uuid.NewString())
		},
		Now: time.Now,
	})
	if err != nil {
		return analyticsmodule.DevelopmentProfileApplicationAPIConfig{}, fmt.Errorf("build development profile application service: %w", err)
	}
	return analyticsmodule.DevelopmentProfileApplicationAPIConfig{
		Service: profileService, Store: profileApplications, Enabled: true,
		CheckoutID: runtimeConfig.LocalCheckoutID, RuntimeID: runtimeConfig.LocalRuntimeID,
		ProfileName: runtimeConfig.DevelopmentProfileName, GraphDigest: runtimeConfig.DevelopmentGraphDigest,
		ProfileDigest: runtimeConfig.DevelopmentProfileDigest, Environment: runtimeConfig.DefaultEnvironment,
		TargetID: targetID, ResolveProjectID: resolveProjectID,
		CurrentPrincipal: func(r *http.Request) (string, bool) {
			principal, ok := accessModule.CurrentPrincipal(r)
			return principal.ID, ok
		},
		Audit: func(ctx context.Context, principalID, projectID, action, metadata string) error {
			record := accessAuditRecorder(accessModule)
			if record == nil {
				return errors.New("development profile audit authority is unavailable")
			}
			return record(ctx, access.AuditEventInput{ProjectID: projectID, PrincipalID: principalID, Action: action, ResourceKind: "project", ResourceID: projectID, Capability: access.CapabilityResourceManage, Status: "succeeded", MetadataJSON: metadata})
		},
	}, nil
}
