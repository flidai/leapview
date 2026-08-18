package runtimefactory

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
)

type deliveryTargetResolverFunc func(context.Context, string) (deployment.DeliveryTarget, error)

func (f deliveryTargetResolverFunc) ResolveDeliveryTarget(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	return f(ctx, targetID)
}

func TestBootstrapTargetResolverResolvesProjectClaimedAfterStartup(t *testing.T) {
	var calls int
	resolver := BootstrapTargetResolver{
		Resolver: deliveryTargetResolverFunc(func(context.Context, string) (deployment.DeliveryTarget, error) {
			return deployment.DeliveryTarget{}, sql.ErrNoRows
		}),
		TargetID:    "target-local",
		Environment: "dev",
		ProjectIDResolver: func(context.Context) (string, error) {
			calls++
			return "project:claimed", nil
		},
	}

	target, err := resolver.ResolveDeliveryTarget(t.Context(), "target-local")
	if err != nil {
		t.Fatalf("ResolveDeliveryTarget() error = %v", err)
	}
	if target.TargetID != "target-local" || target.ProjectID != "project:claimed" || target.Environment != "dev" {
		t.Fatalf("resolved target = %#v, want target-local/project:claimed/dev", target)
	}
	if calls != 1 {
		t.Fatalf("project claim resolver calls = %d, want 1", calls)
	}
}

func TestBootstrapTargetResolverPropagatesProjectClaimReadError(t *testing.T) {
	wantErr := errors.New("claim database unavailable")
	resolver := BootstrapTargetResolver{
		Resolver: deliveryTargetResolverFunc(func(context.Context, string) (deployment.DeliveryTarget, error) {
			return deployment.DeliveryTarget{}, deployment.ErrNotFound
		}),
		TargetID:    "target-local",
		Environment: "dev",
		ProjectIDResolver: func(context.Context) (string, error) {
			return "", wantErr
		},
	}

	_, err := resolver.ResolveDeliveryTarget(t.Context(), "target-local")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveDeliveryTarget() error = %v, want %v", err, wantErr)
	}
}
