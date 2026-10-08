package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type retainedVisualPublicMetrics struct{ queryruntime.Metrics }

func (retainedVisualPublicMetrics) SemanticModel(string) (*semanticmodel.Model, bool) {
	return &semanticmodel.Model{}, true
}

type retainedVisualProjectionMetrics struct {
	retainedVisualPublicMetrics
	authorize func(context.Context, string) error
}

func (m retainedVisualProjectionMetrics) AuthorizeSemanticModelProjection(ctx context.Context, modelID string) error {
	return m.authorize(ctx, modelID)
}

func TestRetainedVisualUsesGovernedProjectionAuthority(t *testing.T) {
	denied := errors.New("projection denied")
	for _, tc := range []struct {
		name             string
		projectionError  error
		missingAuthority bool
		resourceDenied   bool
		credentialDenied bool
		wantCalls        int
		wantError        bool
	}{
		{name: "allowed", wantCalls: 1},
		{name: "projection denied", projectionError: denied, wantCalls: 1, wantError: true},
		{name: "missing projection authority", missingAuthority: true, wantError: true},
		{name: "resource denied", resourceDenied: true, wantError: true},
		{name: "credential denied", credentialDenied: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := 0
			var metrics queryruntime.Metrics = retainedVisualProjectionMetrics{authorize: func(gotContext context.Context, modelID string) error {
				calls++
				if gotContext != ctx || modelID != "semantic:canonical" {
					t.Fatalf("projection authorization context/model = %v/%q, want retained request context and canonical model", gotContext, modelID)
				}
				return tc.projectionError
			}}
			if tc.missingAuthority {
				metrics = retainedVisualPublicMetrics{}
			}
			module := &Module{
				projectID: "project:active",
				resolveResource: func(_ context.Context, scope Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
					if scope.ProjectID != "project:active" || scope.PrincipalID != "principal:owner" || id != "semantic:requested" || kind != projectgraph.KindSemanticModel || capability != access.CapabilityResourceUse {
						t.Fatalf("unexpected resource authorization scope=%+v id=%s kind=%s capability=%s", scope, id, kind, capability)
					}
					if tc.resourceDenied {
						return "", access.ErrForbidden
					}
					return "semantic:canonical", nil
				},
				dashboardMetrics: func(projectID string) (queryruntime.Metrics, bool) {
					if projectID != "project:active" {
						t.Fatalf("metrics project=%s, want active project", projectID)
					}
					return metrics, true
				},
			}
			scope := agent.Scope{ProjectID: "project:untrusted", PrincipalID: "principal:owner"}
			if tc.credentialDenied {
				scope.Credential = agent.CredentialScope{Restricted: true, PermissionProfile: access.PermissionCatalogProfile}
			}
			err := module.authorizeRetainedVisual(ctx, scope, "semantic:requested")
			if (err != nil) != tc.wantError || calls != tc.wantCalls {
				t.Fatalf("error=%v calls=%d, want error=%t calls=%d", err, calls, tc.wantError, tc.wantCalls)
			}
			if tc.projectionError != nil && !errors.Is(err, tc.projectionError) {
				t.Fatalf("projection authority error was lost: %v", err)
			}
		})
	}
}
