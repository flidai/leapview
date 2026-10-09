package connectionbinding

import (
	"context"
	"errors"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"testing"
)

type candidateAdmission struct {
	closed   bool
	releases int
}

func (g *candidateAdmission) Acquire(ctx context.Context) (context.Context, func(), error) {
	if g.closed {
		return nil, nil, ErrProviderUnavailable
	}
	return ctx, func() { g.releases++ }, nil
}
func TestCandidateProviderAdmissionLivesUntilPoolLeasesRelease(t *testing.T) {
	binding := validTargetBinding(t)
	directory := &recordingValidatedPoolDirectory{}
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{Bindings: &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: binding}}, Pools: directory, Authorize: func(context.Context, string, TargetBinding) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	gate := &candidateAdmission{closed: true}
	if err = leaser.ConfigureProviderAdmission(gate); err != nil {
		t.Fatal(err)
	}
	request := RuntimeBindingRequest{Actor: "actor", Identity: servingIdentity(binding.Scope.ProjectID.String(), binding.Scope.Environment, "generation-1"), TargetID: binding.TargetID, Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}}}
	if _, err = leaser.Acquire(t.Context(), request); !errors.Is(err, ErrProviderUnavailable) || len(directory.acquired) != 0 {
		t.Fatal("closed admission performed provider acquisition")
	}
	gate.closed = false
	leases, err := leaser.Acquire(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if gate.releases != 0 {
		t.Fatal("candidate provider lease released before candidate")
	}
	leases.Release()
	leases.Release()
	if gate.releases != 1 || directory.leases[0].releases != 1 {
		t.Fatal("candidate cleanup was not acknowledged exactly once")
	}
}
