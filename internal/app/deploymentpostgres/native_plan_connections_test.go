package deploymentpostgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	deployment "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestNativePlanConnectionsPreserveDefaultAuthorities(t *testing.T) {
	defaults := &nativeConnectionLeaser{}
	coordinator := &NativeBuildCoordinator{bindingEvidence: defaults, connections: defaults}
	selected, err := coordinator.selectPlanConnections(t.Context(), deployment.DeliveryPlan{})
	if err != nil || selected.BindingEvidence != defaults || selected.Connections != defaults {
		t.Fatalf("default authority selection = %+v, %v", selected, err)
	}
	// Existing no-connection fixtures may leave both defaults absent.
	selected, err = (&NativeBuildCoordinator{}).selectPlanConnections(t.Context(), deployment.DeliveryPlan{})
	if err != nil || selected.BindingEvidence != nil || selected.Connections != nil {
		t.Fatalf("absent legacy defaults = %+v, %v", selected, err)
	}
}

func TestNativePlanConnectionsFailClosedWithoutFallingBack(t *testing.T) {
	defaults := &nativeConnectionLeaser{}
	var typedNil *nativeConnectionLeaser
	denied := errors.New("exact plan connection admission denied")
	for _, test := range []struct {
		name string
		pair NativePlanConnectionAuthorities
		err  error
	}{
		{name: "denied", pair: NativePlanConnectionAuthorities{BindingEvidence: defaults, Connections: defaults}, err: denied},
		{name: "missing evidence", pair: NativePlanConnectionAuthorities{Connections: defaults}},
		{name: "missing leaser", pair: NativePlanConnectionAuthorities{BindingEvidence: defaults}},
		{name: "typed nil evidence", pair: NativePlanConnectionAuthorities{BindingEvidence: typedNil, Connections: defaults}},
		{name: "typed nil leaser", pair: NativePlanConnectionAuthorities{BindingEvidence: defaults, Connections: typedNil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &NativeBuildCoordinator{bindingEvidence: defaults, connections: defaults,
				planConnections: func(context.Context, deployment.DeliveryPlan) (NativePlanConnectionAuthorities, error) {
					return test.pair, test.err
				}}
			selected, err := coordinator.selectPlanConnections(t.Context(), deployment.DeliveryPlan{ID: "exact-plan"})
			if err == nil || selected != (NativePlanConnectionAuthorities{}) {
				t.Fatalf("configured failure fell back to defaults: %+v, %v", selected, err)
			}
			if test.err != nil && !errors.Is(err, denied) {
				t.Fatalf("selector error identity lost: %v", err)
			}
			if test.err == nil && !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
				t.Fatalf("missing authority not classified unavailable: %v", err)
			}
		})
	}
}

func TestNativeBuildSelectsExactPersistedPlanBeforeMaterialization(t *testing.T) {
	coordinator, request, artifacts, _ := nativeBuildPlanCoordinatorFixture(t, false)
	persisted, err := coordinator.loadBuildPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("exact stored preparation revoked")
	calls := 0
	coordinator.planConnections = func(_ context.Context, plan deployment.DeliveryPlan) (NativePlanConnectionAuthorities, error) {
		calls++
		if !reflect.DeepEqual(plan, persisted.DeliveryPlan) {
			t.Fatalf("connection selection used a different plan: %+v", plan)
		}
		return NativePlanConnectionAuthorities{}, denied
	}
	_, err = coordinator.BuildPlan(t.Context(), request)
	if !errors.Is(err, denied) || calls != 1 || artifacts.materializeCnt != 0 {
		t.Fatalf("revoked selection: err=%v calls=%d materialized=%d", err, calls, artifacts.materializeCnt)
	}
}

func TestNativePlanConnectionsKeepSelectedPairForEvidenceAndPhysicalWork(t *testing.T) {
	evidence := []deployment.CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7')}}
	resolver := &nativeConnectionLeaser{leases: &nativeConnectionLeases{evidence: evidence}}
	leases := &nativeConnectionLeases{evidence: evidence}
	leaser := &nativeConnectionLeaser{leases: leases}
	defaults := &nativeConnectionLeaser{err: errors.New("default authority must not be used")}
	plan := deployment.DeliveryPlan{ID: "retained-plan", TargetID: "exact-target", SourceDigest: createPlanTestDigest('a')}
	calls := 0
	coordinator := &NativeBuildCoordinator{bindingEvidence: defaults, connections: defaults,
		planConnections: func(_ context.Context, received deployment.DeliveryPlan) (NativePlanConnectionAuthorities, error) {
			calls++
			if !reflect.DeepEqual(received, plan) {
				t.Fatal("selected a different plan")
			}
			return NativePlanConnectionAuthorities{BindingEvidence: resolver, Connections: leaser}, nil
		},
	}
	selected, err := coordinator.selectPlanConnections(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	request := deployment.CandidateConnectionRequest{Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}}
	digest, err := resolveNativeCandidateBindingDigest(t.Context(), selected.BindingEvidence, request)
	if err != nil {
		t.Fatal(err)
	}
	input := nativePhysicalFixtureInput(t)
	environment := nativePhysicalEnvironment(t, input)
	physical, acquired, err := buildNativePhysicalWithCandidateBindingsEvidence(t.Context(), selected.Connections, request, digest, input, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return environment, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if physical.SnapshotID == 0 || !reflect.DeepEqual(acquired, evidence) || calls != 1 || resolver.resolveCalls != 1 || leaser.calls != 1 || leases.closeCalls != 1 || defaults.calls != 0 || defaults.resolveCalls != 0 {
		t.Fatalf("selected pair not retained: physical=%+v calls=%d resolve=%d acquire=%d close=%d defaults=%+v", physical, calls, resolver.resolveCalls, leaser.calls, leases.closeCalls, defaults)
	}
}

type nativePlanConnectionRecoveryContract struct{ NativeBuildContractResolver }

func (c nativePlanConnectionRecoveryContract) Resolve(ctx context.Context, request NativeBuildContractRequest) (NativeBuildContract, error) {
	contract, err := c.NativeBuildContractResolver.Resolve(ctx, request)
	contract.Catalog.CatalogID = "catalog-plan-connection-selection"
	return contract, err
}

type nativePlanConnectionSuccessorProbe struct {
	deploymentmodule.NativeBuildOperationAuthority
	deploymentmodule.NativeBuildOperationSuccessorAuthority
	calls int
}

func (p *nativePlanConnectionSuccessorProbe) CurrentSuccessorAttempt(context.Context, string) (deploymentmodule.NativeOperationSuccessor, bool, error) {
	p.calls++
	return deploymentmodule.NativeOperationSuccessor{}, false, errors.New("revoked selector must run before successor dispatch")
}

func TestNativePlanConnectionsDenyEveryRecoveryAndSuccessorContinuation(t *testing.T) {
	coordinator, request, _, _ := nativeBuildPlanCoordinatorFixture(t, false)
	persisted, err := coordinator.loadBuildPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.contract = nativePlanConnectionRecoveryContract{coordinator.contract}
	coordinator.connections = &nativeConnectionLeaser{}
	probe := &nativePlanConnectionSuccessorProbe{NativeBuildOperationAuthority: coordinator.operations}
	coordinator.operations = probe
	denied := errors.New("stored exact plan authority revoked")
	calls := 0
	coordinator.planConnections = func(_ context.Context, plan deployment.DeliveryPlan) (NativePlanConnectionAuthorities, error) {
		calls++
		if !reflect.DeepEqual(plan, persisted.DeliveryPlan) {
			t.Fatal("continuation used a different plan")
		}
		return NativePlanConnectionAuthorities{}, denied
	}
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{"root recovery", func() error {
			_, err := coordinator.recoverIndeterminateNativeBuild(t.Context(), request, "request-digest", NativeBuildOperationReservationResult{}, persisted)
			return err
		}},
		{"successor execution", func() error {
			_, err := coordinator.executeNativeBuildSuccessor(t.Context(), request, "request-digest", NativeBuildOperationReservationResult{}, persisted, NativeBuildContract{}, release.CandidateArtifactSet{}, NativeBuildSuccessorAdmissionResult{})
			return err
		}},
		{"successor recovery completion", func() error {
			_, err := coordinator.completeRecoveredNativeBuildSuccessor(t.Context(), request, "request-digest", NativeBuildOperationReservationResult{}, persisted, NativeBuildContract{}, release.CandidateArtifactSet{}, NativeBuildRecoveryPreparationResult{}, deploymentnative.BuildArtifactBinding{}, NativePhysicalBuildEvidence{}, deploymentmodule.NativeOperationSuccessor{})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := calls
			if err := test.run(); !errors.Is(err, denied) || calls != before+1 || probe.calls != 0 {
				t.Fatalf("continuation fallback: err=%v calls=%d", err, calls-before)
			}
		})
	}
}
