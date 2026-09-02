package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type nativeConnectionLeaser struct {
	request      deployment.CandidateConnectionRequest
	leases       deployment.CandidateConnectionLeases
	calls        int
	resolveCalls int
	err          error
}

func (leaser *nativeConnectionLeaser) Acquire(_ context.Context, request deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
	leaser.calls++
	leaser.request = request
	return leaser.leases, leaser.err
}

func (leaser *nativeConnectionLeaser) Resolve(_ context.Context, request deployment.CandidateConnectionRequest) ([]deployment.CandidateConnectionEvidence, error) {
	leaser.resolveCalls++
	leaser.request = request
	if leaser.err != nil {
		return nil, leaser.err
	}
	if nativeBuildAuthorityNil(leaser.leases) {
		return nil, nil
	}
	return leaser.leases.Evidence(), nil
}

type nativeConnectionLeases struct {
	evidence   []deployment.CandidateConnectionEvidence
	closeCalls int
	closeErr   error
}

func (leases *nativeConnectionLeases) Evidence() []deployment.CandidateConnectionEvidence {
	return append([]deployment.CandidateConnectionEvidence(nil), leases.evidence...)
}

func (leases *nativeConnectionLeases) Close() error {
	leases.closeCalls++
	return leases.closeErr
}

func TestAcquireNativeCandidateBindingsUsesExactLeaseEvidence(t *testing.T) {
	evidence := []deployment.CandidateConnectionEvidence{{
		BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres",
		Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7'),
	}}
	leases := &nativeConnectionLeases{evidence: evidence}
	leaser := &nativeConnectionLeaser{leases: leases}
	request := deployment.CandidateConnectionRequest{
		CandidateID: "candidate-native", Actor: "principal-native", TargetID: "target-native",
		Identity:     projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID("project_native"), Environment: "prod", GenerationID: "generation-native"},
		Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}},
	}
	gotLeases, gotDigest, err := acquireNativeCandidateBindings(t.Context(), leaser, request)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if gotLeases != leases || gotDigest != wantDigest || leaser.calls != 1 || leaser.request.CandidateID != request.CandidateID || leaser.request.Identity != request.Identity {
		t.Fatalf("binding resolution = leases %T digest %q calls %d request %+v, want exact lease/digest/request", gotLeases, gotDigest, leaser.calls, leaser.request)
	}
}

func TestAcquireNativeCandidateBindingsUsesCanonicalEmptyFingerprintWithoutAuthority(t *testing.T) {
	_, got, err := acquireNativeCandidateBindings(t.Context(), nil, deployment.CandidateConnectionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := deployment.BindingFingerprint(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("empty binding fingerprint = %q, want %q", got, want)
	}
}

func TestResolveNativeCandidateBindingDigestUsesEvidenceWithoutAcquiringLease(t *testing.T) {
	evidence := []deployment.CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7')}}
	resolver := &nativeConnectionLeaser{leases: &nativeConnectionLeases{evidence: evidence}}
	request := deployment.CandidateConnectionRequest{Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}}
	got, err := resolveNativeCandidateBindingDigest(t.Context(), resolver, request)
	if err != nil {
		t.Fatal(err)
	}
	want, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || resolver.resolveCalls != 1 || resolver.calls != 0 {
		t.Fatalf("resolved binding = %q calls resolve/acquire %d/%d, want %q and 1/0", got, resolver.resolveCalls, resolver.calls, want)
	}
}

func TestNativeCandidateBindingEvidenceMustCoverRequirements(t *testing.T) {
	request := deployment.CandidateConnectionRequest{Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}}
	resolver := &nativeConnectionLeaser{leases: &nativeConnectionLeases{}}
	if _, err := resolveNativeCandidateBindingDigest(t.Context(), resolver, request); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("incomplete resolver evidence error = %v, want delivery conflict", err)
	}
	leaser := &nativeConnectionLeaser{leases: &nativeConnectionLeases{}}
	if leases, _, err := acquireNativeCandidateBindings(t.Context(), leaser, request); !errors.Is(err, deployment.ErrDeliveryConflict) || leases != nil {
		t.Fatalf("incomplete lease evidence = (%T, %v), want nil delivery conflict", leases, err)
	}
}

func TestAcquireNativeCandidateBindingsFailsClosedWhenRequiredAuthorityIsMissing(t *testing.T) {
	_, _, err := acquireNativeCandidateBindings(t.Context(), nil, deployment.CandidateConnectionRequest{
		Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}},
	})
	if !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("missing connection authority error = %v, want input unavailable", err)
	}
}

func TestCloseNativeCandidateBindingsClassifiesReleaseFailureAsConflict(t *testing.T) {
	leases := &nativeConnectionLeases{closeErr: errors.New("release failed")}
	if err := closeNativeCandidateBindings(leases); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("close error = %v, want delivery conflict", err)
	}
	if leases.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", leases.closeCalls)
	}
}

func TestBuildNativePhysicalWithCandidateBindingsScopesLeaseToPhysicalWork(t *testing.T) {
	evidence := []deployment.CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7')}}
	leases := &nativeConnectionLeases{evidence: evidence}
	leaser := &nativeConnectionLeaser{leases: leases}
	request := deployment.CandidateConnectionRequest{Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}}
	digest, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		t.Fatal(err)
	}
	input := nativePhysicalFixtureInput(t)
	environment := nativePhysicalEnvironment(t, input)
	physical, err := buildNativePhysicalWithCandidateBindings(t.Context(), leaser, request, digest, input, NativePhysicalBuildEnvironmentFactoryFunc(func(_ context.Context, _ catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		if leases.closeCalls != 0 {
			t.Fatal("candidate connections closed before physical environment opened")
		}
		return environment, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if physical.SnapshotID == 0 || leaser.calls != 1 || leases.closeCalls != 1 {
		t.Fatalf("physical/connection lifecycle = snapshot %d acquire %d close %d, want nonzero/1/1", physical.SnapshotID, leaser.calls, leases.closeCalls)
	}
}

func TestBuildNativePhysicalWithCandidateBindingsEvidenceAllowsNoBindings(t *testing.T) {
	input := nativePhysicalFixtureInput(t)
	emptyDigest, err := deployment.BindingFingerprint(nil)
	if err != nil {
		t.Fatal(err)
	}
	physical, evidence, err := buildNativePhysicalWithCandidateBindingsEvidence(
		t.Context(), nil, deployment.CandidateConnectionRequest{}, emptyDigest, input,
		NativePhysicalBuildEnvironmentFactoryFunc(func(_ context.Context, _ catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
			return nativePhysicalEnvironment(t, input), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if physical.SnapshotID == 0 {
		t.Fatal("physical build returned no snapshot")
	}
	if len(evidence) != 0 {
		t.Fatalf("binding evidence = %#v, want empty", evidence)
	}
}

func TestBuildNativePhysicalWithCandidateBindingsClosesBeforeReturningFailure(t *testing.T) {
	evidence := []deployment.CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7')}}
	leases := &nativeConnectionLeases{evidence: evidence}
	leaser := &nativeConnectionLeaser{leases: leases}
	request := deployment.CandidateConnectionRequest{Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}}
	digest, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		t.Fatal(err)
	}
	openErr := errors.New("physical open failed")
	_, err = buildNativePhysicalWithCandidateBindings(t.Context(), leaser, request, digest, nativePhysicalFixtureInput(t), NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		if leases.closeCalls != 0 {
			t.Fatal("candidate connections closed before physical open attempt")
		}
		return nil, openErr
	}))
	if !errors.Is(err, openErr) || !NativePhysicalBuildFailureIsIndeterminate(err) || leases.closeCalls != 1 {
		t.Fatalf("physical failure = %v close=%d, want indeterminate wrapped error and one close", err, leases.closeCalls)
	}
}

func TestBuildNativePhysicalWithCandidateBindingsRejectsDriftBeforeOpen(t *testing.T) {
	evidence := []deployment.CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7')}}
	leases := &nativeConnectionLeases{evidence: evidence}
	leaser := &nativeConnectionLeaser{leases: leases}
	request := deployment.CandidateConnectionRequest{Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}}
	_, err := buildNativePhysicalWithCandidateBindings(t.Context(), leaser, request, createPlanTestDigest('9'), nativePhysicalFixtureInput(t), NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		t.Fatal("physical environment opened after binding drift")
		return nil, nil
	}))
	if !errors.Is(err, deployment.ErrDeliveryConflict) || !NativePhysicalBuildFailureIsDeterministic(err) || leases.closeCalls != 1 {
		t.Fatalf("binding drift failure = %v close=%d, want deterministic conflict and one close", err, leases.closeCalls)
	}
}
