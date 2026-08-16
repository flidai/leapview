package connectionbinding

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectResolverNeverFallsBackAfterAuthoritativeProviderDenial(t *testing.T) {
	selection, err := NewResolverSelection(ResolverSelectionInput{
		TargetID: "target-prod", ProjectID: "project_sales", Environment: "prod", TargetClass: TargetProduction, Kind: ResolverInfisical,
	})
	require.NoError(t, err)
	authoritative := &countingCredentialResolver{err: ErrCredentialDenied}
	development := &countingCredentialResolver{}
	resolver, err := SelectResolver(selection, ResolverSet{Infisical: authoritative, Environment: development})
	require.NoError(t, err)
	if _, err := resolver.Resolve(context.Background(), CredentialReference{}); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("Resolve() error = %v", err)
	}
	if authoritative.calls != 1 || development.calls != 0 {
		t.Fatalf("authoritative calls=%d development calls=%d", authoritative.calls, development.calls)
	}
}

func TestSelectResolverRequiresTheExplicitlySelectedProvider(t *testing.T) {
	selection, err := NewResolverSelection(ResolverSelectionInput{
		TargetID: "target-prod", ProjectID: "project_sales", Environment: "prod", TargetClass: TargetProduction, Kind: ResolverInfisical,
	})
	require.NoError(t, err)
	if _, err := SelectResolver(selection, ResolverSet{Environment: &countingCredentialResolver{}}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("SelectResolver() error = %v", err)
	}
}

type countingCredentialResolver struct {
	calls int
	err   error
}

func (resolver *countingCredentialResolver) Resolve(context.Context, CredentialReference) (CredentialSnapshot, error) {
	resolver.calls++
	return CredentialSnapshot{}, resolver.err
}
