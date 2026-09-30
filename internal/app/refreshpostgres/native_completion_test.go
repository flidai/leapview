package refreshpostgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
)

type nativeCompletionReaderStub struct {
	generation deploymentpostgres.DeliveryGeneration
	err        error
	calls      int
	events     *[]string
}

func (reader *nativeCompletionReaderStub) LoadGeneration(context.Context, string) (deploymentpostgres.DeliveryGeneration, error) {
	reader.calls++
	if reader.events != nil {
		*reader.events = append(*reader.events, "load")
	}
	return reader.generation, reader.err
}

type nativeCompletionHostStub struct {
	prepared           *runtimehost.Prepared
	prepareErr         error
	activateErr        error
	prepareGeneration  string
	prepareCandidate   string
	activateCalls      int
	events             *[]string
	activateCompletion bool
}

func (host *nativeCompletionHostStub) PrepareSealedActivation(_ context.Context, generationID, candidateID string) (*runtimehost.Prepared, error) {
	if host.events != nil {
		*host.events = append(*host.events, "prepare")
	}
	host.prepareGeneration, host.prepareCandidate = generationID, candidateID
	return host.prepared, host.prepareErr
}

func (host *nativeCompletionHostStub) ActivatePreparedContext(_ context.Context, prepared *runtimehost.Prepared, complete func() error) error {
	if host.events != nil {
		*host.events = append(*host.events, "activate")
	}
	host.activateCalls++
	if prepared != host.prepared {
		return errors.New("unexpected prepared runtime")
	}
	if host.activateCompletion {
		if host.events != nil {
			*host.events = append(*host.events, "complete")
		}
		if err := complete(); err != nil {
			return err
		}
	}
	return host.activateErr
}

func TestNativeCanonicalCompletionCoordinatesDurableActivation(t *testing.T) {
	var events []string
	reader := &nativeCompletionReaderStub{
		generation: deploymentpostgres.DeliveryGeneration{
			GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result",
		},
		events: &events,
	}
	prepared := &runtimehost.Prepared{}
	host := &nativeCompletionHostStub{prepared: prepared, events: &events, activateCompletion: true}
	wantIdentity := projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_result"}
	coordinator, err := NewNativeCanonicalCompletionCoordinator("target_prod", reader, func(_ context.Context, candidate deploymentdomain.Deployment) error {
		events = append(events, "ownership")
		if candidate.ServingIdentity != wantIdentity {
			t.Fatalf("ownership candidate identity = %#v, want %#v", candidate.ServingIdentity, wantIdentity)
		}
		return nil
	}, host)
	if err != nil {
		t.Fatal(err)
	}
	job := refreshrun.JobRecord{Identity: projectgraph.ServingIdentity{ProjectID: wantIdentity.ProjectID, Environment: wantIdentity.Environment, GenerationID: "generation_base"}}
	result := refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"}
	completeCalls := 0
	if err := coordinator(t.Context(), job, result, func() error {
		completeCalls++
		return nil
	}); err != nil {
		t.Fatalf("coordinate completion: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"load", "ownership", "prepare", "activate", "complete"}) {
		t.Fatalf("completion order = %v", events)
	}
	if completeCalls != 1 || host.activateCalls != 1 || host.prepareGeneration != result.ServingStateID || host.prepareCandidate != reader.generation.CandidateID {
		t.Fatalf("completion calls=%d activation calls=%d prepared=%q/%q", completeCalls, host.activateCalls, host.prepareGeneration, host.prepareCandidate)
	}
}

func TestNativeCanonicalCompletionFailsClosedAtBoundaries(t *testing.T) {
	readErr := errors.New("generation read failed")
	ownershipErr := errors.New("ownership denied")
	prepareErr := errors.New("prepare failed")
	activateErr := errors.New("activate failed")
	for _, test := range []struct {
		name              string
		result            refreshrun.CanonicalRefreshResult
		generation        deploymentpostgres.DeliveryGeneration
		readErr           error
		ownershipErr      error
		prepareErr        error
		prepared          bool
		completeErr       error
		complete          bool
		activateErr       error
		wantErr           error
		wantCompleteCalls int
		wantCalls         []string
	}{
		{
			name:       "result generation mismatch",
			result:     refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_other", PlanID: "plan_result"},
			generation: deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result"},
		},
		{
			name:    "reader failure",
			result:  refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			readErr: readErr, wantErr: readErr, wantCalls: []string{"load"},
		},
		{
			name:       "generation tuple mismatch",
			result:     refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			generation: deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_other", PlanID: "plan_result", CandidateID: "candidate_result"},
			wantCalls:  []string{"load"},
		},
		{
			name:         "ownership failure",
			result:       refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			generation:   deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result"},
			ownershipErr: ownershipErr, wantErr: ownershipErr, wantCalls: []string{"load", "ownership"},
		},
		{
			name:       "prepare failure",
			result:     refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			generation: deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result"},
			prepareErr: prepareErr, wantErr: prepareErr, wantCalls: []string{"load", "ownership", "prepare"},
		},
		{
			name:       "nil prepared runtime",
			result:     refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			generation: deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result"},
			wantCalls:  []string{"load", "ownership", "prepare"},
		},
		{
			name:       "activation failure",
			result:     refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			generation: deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result"},
			prepared:   true, activateErr: activateErr, wantErr: activateErr, wantCalls: []string{"load", "ownership", "prepare", "activate"},
		},
		{
			name:       "durable completion failure",
			result:     refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"},
			generation: deploymentpostgres.DeliveryGeneration{GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result"},
			prepared:   true, complete: true, completeErr: readErr, wantErr: readErr, wantCompleteCalls: 1,
			wantCalls: []string{"load", "ownership", "prepare", "activate", "complete"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			reader := &nativeCompletionReaderStub{generation: test.generation, err: test.readErr, events: &events}
			var prepared *runtimehost.Prepared
			if test.prepared {
				prepared = &runtimehost.Prepared{}
			}
			host := &nativeCompletionHostStub{prepared: prepared, prepareErr: test.prepareErr, activateErr: test.activateErr, events: &events, activateCompletion: test.complete}
			coordinator, err := NewNativeCanonicalCompletionCoordinator("target_prod", reader, func(context.Context, deploymentdomain.Deployment) error {
				events = append(events, "ownership")
				return test.ownershipErr
			}, host)
			if err != nil {
				t.Fatal(err)
			}
			job := refreshrun.JobRecord{Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_base"}}
			completeCalls := 0
			callErr := coordinator(t.Context(), job, test.result, func() error { completeCalls++; return test.completeErr })
			if callErr == nil {
				t.Fatal("completion unexpectedly succeeded")
			}
			if test.wantErr != nil && !errors.Is(callErr, test.wantErr) {
				t.Fatalf("completion error=%v, want wrapped %v", callErr, test.wantErr)
			}
			if !reflect.DeepEqual(events, test.wantCalls) {
				t.Fatalf("completion calls=%v, want %v", events, test.wantCalls)
			}
			if completeCalls != test.wantCompleteCalls {
				t.Fatalf("completion callback ran %d times on failure, want %d", completeCalls, test.wantCompleteCalls)
			}
		})
	}
}

func TestNativeCanonicalCompletionRejectsMissingInvocationInputs(t *testing.T) {
	reader := &nativeCompletionReaderStub{generation: deploymentpostgres.DeliveryGeneration{
		GenerationID: "generation_result", TargetID: "target_prod", PlanID: "plan_result", CandidateID: "candidate_result",
	}}
	host := &nativeCompletionHostStub{prepared: &runtimehost.Prepared{}}
	coordinator, err := NewNativeCanonicalCompletionCoordinator("target_prod", reader, func(context.Context, deploymentdomain.Deployment) error { return nil }, host)
	if err != nil {
		t.Fatal(err)
	}
	job := refreshrun.JobRecord{Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_base"}}
	result := refreshrun.CanonicalRefreshResult{ServingStateID: "generation_result", NativeGenerationID: "generation_result", PlanID: "plan_result"}
	if err := coordinator(nil, job, result, func() error { return nil }); err == nil {
		t.Fatal("completion accepted a nil context")
	}
	if err := coordinator(t.Context(), job, result, nil); err == nil {
		t.Fatal("completion accepted a nil callback")
	}
	job.Identity.ProjectID = " project_demo "
	if err := coordinator(t.Context(), job, result, func() error { return nil }); err == nil {
		t.Fatal("completion accepted a noncanonical identity")
	}
	if reader.calls != 0 || host.activateCalls != 0 {
		t.Fatalf("invalid invocation reached reader/host: reads=%d activations=%d", reader.calls, host.activateCalls)
	}
}

func TestNativeCanonicalCompletionConstructorRejectsMissingDependencies(t *testing.T) {
	reader := &nativeCompletionReaderStub{}
	host := &nativeCompletionHostStub{prepared: &runtimehost.Prepared{}}
	validator := func(context.Context, deploymentdomain.Deployment) error { return nil }
	for _, test := range []struct {
		name     string
		targetID string
		reader   nativeCompletionGenerationReader
		validate func(context.Context, deploymentdomain.Deployment) error
		host     nativeCompletionRuntimeHost
	}{
		{name: "noncanonical target", targetID: " target_prod ", reader: reader, validate: validator, host: host},
		{name: "missing reader", targetID: "target_prod", validate: validator, host: host},
		{name: "missing ownership validator", targetID: "target_prod", reader: reader, host: host},
		{name: "missing runtime host", targetID: "target_prod", reader: reader, validate: validator},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewNativeCanonicalCompletionCoordinator(test.targetID, test.reader, test.validate, test.host); err == nil {
				t.Fatal("constructor accepted missing or noncanonical dependency")
			}
		})
	}
}
