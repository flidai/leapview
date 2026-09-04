package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identityledger "github.com/flidai/leapview/internal/project/identityledger/module"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

type identityLifecycleRepositoryFake struct {
	transition      identityledger.Transition
	published       identityledger.Transition
	observed        string
	outcomes        []identityledger.Outcome
	events          *[]string
	planErr         error
	prepareErr      error
	loadErr         error
	referenceErr    error
	references      []identityledger.DurableReference
	referenceResult identityledger.DurableReference
	activatedActor  string
	restoreCalls    int
	restoredIDs     []projectgraph.ResourceID
}

func (f *identityLifecycleRepositoryFake) ReconcileReferences(_ context.Context, instanceID string, references []identityledger.DurableReference) ([]identityledger.DurableReference, error) {
	f.event("references")
	if f.referenceErr != nil {
		return nil, f.referenceErr
	}
	normalized, err := identityledger.NormalizeReferences(instanceID, references)
	if err != nil {
		return nil, err
	}
	f.references = append(f.references, normalized...)
	if f.referenceResult.ReferenceID != "" {
		return []identityledger.DurableReference{f.referenceResult}, nil
	}
	return normalized, nil
}

func (f *identityLifecycleRepositoryFake) event(value string) {
	if f.events != nil {
		*f.events = append(*f.events, value)
	}
}

func (f *identityLifecycleRepositoryFake) PrepareTransition(_ context.Context, input identityledger.Transition) (identityledger.Transition, error) {
	f.event("prepare")
	if f.prepareErr != nil {
		return identityledger.Transition{}, f.prepareErr
	}
	if f.transition.TransitionID == "" {
		f.transition = input
		f.transition.Phase = identityledger.PhasePrepared
		f.transition.Error = ""
	}
	if !identityledger.SameTransitionEvidence(f.transition, input) {
		return identityledger.Transition{}, identityledger.ErrTransitionConflict
	}
	return f.transition, nil
}

func (f *identityLifecycleRepositoryFake) LoadTransition(_ context.Context, _, _ string) (identityledger.Transition, error) {
	f.event("load")
	if f.loadErr != nil {
		return identityledger.Transition{}, f.loadErr
	}
	return f.transition, nil
}

func (f *identityLifecycleRepositoryFake) AdvanceTransition(_ context.Context, _, _ string, expected, next identityledger.TransitionPhase, phaseError string) (identityledger.Transition, error) {
	f.event("advance:" + string(expected) + "->" + string(next))
	if f.transition.Phase != expected && f.transition.Phase != next {
		return identityledger.Transition{}, identityledger.ErrPhaseConflict
	}
	f.transition.Phase = next
	f.transition.Error = phaseError
	return f.transition, nil
}

func (f *identityLifecycleRepositoryFake) Plan(_ context.Context, candidate identityledger.Candidate) (identityledger.Plan, error) {
	f.event("plan")
	if f.planErr != nil {
		return identityledger.Plan{}, f.planErr
	}
	return identityledger.Plan{InstanceID: candidate.InstanceID, ObservedBundleID: f.observed, CandidateBundleID: candidate.BundleID, Outcomes: f.outcomes}, nil
}

func (f *identityLifecycleRepositoryFake) Activate(_ context.Context, candidate identityledger.Candidate) (identityledger.Plan, error) {
	f.event("activate")
	f.activatedActor = candidate.ActorID
	f.observed = candidate.BundleID
	return identityledger.Plan{InstanceID: candidate.InstanceID, ObservedBundleID: candidate.ExpectedBundleID, CandidateBundleID: candidate.BundleID}, nil
}

func (f *identityLifecycleRepositoryFake) RestoreAndActivate(_ context.Context, request identityledger.Restore) (identityledger.Plan, error) {
	f.event("restore")
	f.restoreCalls++
	f.restoredIDs = append([]projectgraph.ResourceID(nil), request.AuthoredIDs...)
	f.observed = request.Candidate.BundleID
	return identityledger.Plan{InstanceID: request.Candidate.InstanceID, ObservedBundleID: request.Candidate.BundleID, CandidateBundleID: request.Candidate.BundleID}, nil
}

func (f *identityLifecycleRepositoryFake) Rollback(_ context.Context, request identityledger.Rollback) (identityledger.Plan, error) {
	f.event("rollback")
	f.observed = request.BundleID
	return identityledger.Plan{InstanceID: request.InstanceID, ObservedBundleID: request.ExpectedBundleID, CandidateBundleID: request.BundleID}, nil
}

func (f *identityLifecycleRepositoryFake) PublishedBundleTransition(_ context.Context, _, _ string) (identityledger.Transition, error) {
	f.event("published")
	return f.published, nil
}

func (f *identityLifecycleRepositoryFake) BundlePublishTransition(_ context.Context, _, _ string) (identityledger.Transition, error) {
	f.event("publish-evidence")
	return f.published, nil
}

type identityLifecycleSealedFake struct {
	publishResult        deployment.PublicationIntent
	rollbackResult       deployment.RollbackResult
	publishErr           error
	rollbackErr          error
	publishCalls         int
	rollbackCalls        int
	publishActor         string
	publishCommitCalls   int
	publicationCommitted bool
	events               *[]string
}

type identityLifecycleBasicSealedFake struct{}

func (identityLifecycleBasicSealedFake) Publish(context.Context, sealedcontrol.PublishRequest) (deployment.PublicationIntent, error) {
	return deployment.PublicationIntent{}, nil
}

func (identityLifecycleBasicSealedFake) Rollback(context.Context, sealedcontrol.RollbackRequest) (deployment.RollbackResult, error) {
	return deployment.RollbackResult{}, nil
}

func (f *identityLifecycleSealedFake) Publish(ctx context.Context, request sealedcontrol.PublishRequest) (deployment.PublicationIntent, error) {
	return f.PublishWithActivation(ctx, request, nil)
}

func (f *identityLifecycleSealedFake) PublishWithActivation(ctx context.Context, request sealedcontrol.PublishRequest, activate sealedcontrol.PublicationActivation) (deployment.PublicationIntent, error) {
	f.publishActor = request.ActorID
	if f.events != nil {
		*f.events = append(*f.events, "sealed-preflight")
	}
	if f.publishErr != nil {
		f.publishCalls++
		return f.publishResult, f.publishErr
	}
	if activate != nil {
		if err := activate(ctx, func() error {
			if !f.publicationCommitted {
				f.publishCommitCalls++
				f.publicationCommitted = true
				if f.events != nil {
					*f.events = append(*f.events, "sealed-commit")
				}
			}
			return nil
		}); err != nil {
			return f.publishResult, err
		}
	}
	f.publishCalls++
	if f.events != nil {
		*f.events = append(*f.events, "sealed-publish")
	}
	return f.publishResult, nil
}

func (f *identityLifecycleSealedFake) Rollback(_ context.Context, _ sealedcontrol.RollbackRequest) (deployment.RollbackResult, error) {
	f.rollbackCalls++
	if f.events != nil {
		*f.events = append(*f.events, "sealed-rollback")
	}
	return f.rollbackResult, f.rollbackErr
}

var _ deploymentmodule.SealedCoordinator = (*identityLifecycleSealedFake)(nil)

func TestBuildIdentityCandidateUsesServingGenerationAndExactInputs(t *testing.T) {
	graph := identityLifecycleGraph(t)
	artifacts := release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{Identity: projectgraph.ServingIdentity{ProjectID: graph.ProjectID(), GenerationID: "generation-next"}}, Compiler: release.CandidateCompilerEvidence{Graph: graph}}
	candidate, err := BuildIdentityCandidate(IdentityCandidateInput{InstanceID: "instance-1", ExpectedBaseGenerationID: "generation-current", ActorID: "actor-1", Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.InstanceID != "instance-1" || candidate.BundleID != "generation-next" || candidate.ExpectedBundleID != "generation-current" || candidate.ActorID != "actor-1" {
		t.Fatalf("candidate identity = %#v", candidate)
	}
	want := []identityledger.Resource{{AuthoredID: "model_orders", Kind: projectgraph.KindModel}, {AuthoredID: "source_orders", Kind: projectgraph.KindSource}}
	if !reflect.DeepEqual(candidate.Resources, want) {
		t.Fatalf("candidate resources = %#v, want %#v", candidate.Resources, want)
	}
}

func TestPrepareIdentityPublishTransitionPlansBeforePreparingAndRejectsBlockingOutcomes(t *testing.T) {
	graph := identityLifecycleGraph(t)
	artifacts := release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{Identity: projectgraph.ServingIdentity{ProjectID: graph.ProjectID(), GenerationID: "generation-next"}}, Compiler: release.CandidateCompilerEvidence{Graph: graph}}
	for _, test := range []struct {
		name    string
		outcome identityledger.OutcomeKind
		wantErr error
		prepare bool
	}{
		{name: "collision", outcome: identityledger.OutcomeCollision, wantErr: identityledger.ErrKindConflict},
		{name: "restore", outcome: identityledger.OutcomeRestoreRequired, wantErr: identityledger.ErrRestoreRequired},
		{name: "clear", prepare: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			repository := &identityLifecycleRepositoryFake{events: &events, observed: "generation-current"}
			if test.outcome != "" {
				repository.outcomes = []identityledger.Outcome{{AuthoredID: "source_orders", Kind: projectgraph.KindSource, Outcome: test.outcome, Detail: "test evidence"}}
			}
			transition, err := PrepareIdentityPublishTransition(t.Context(), repository, IdentityPublishPreparationInput{IdentityCandidateInput: IdentityCandidateInput{InstanceID: "instance-1", ExpectedBaseGenerationID: "generation-current", ActorID: "actor-1", Artifacts: artifacts}, CandidateID: "candidate-1", Reason: "ready"})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				if len(events) != 1 || events[0] != "plan" {
					t.Fatalf("events = %#v, want only plan", events)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !test.prepare || len(events) != 2 || events[0] != "plan" || events[1] != "prepare" {
				t.Fatalf("events = %#v, want plan then prepare", events)
			}
			if transition.TransitionID != "identity-publish:candidate-1" || transition.GraphDigest != graph.Digest() || transition.BundleID != "generation-next" {
				t.Fatalf("transition = %#v", transition)
			}
		})
	}
}

func TestPlanIdentityCandidateRequiresExactRestoreTombstones(t *testing.T) {
	graph := identityLifecycleGraph(t)
	artifacts := release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{Identity: projectgraph.ServingIdentity{ProjectID: graph.ProjectID(), GenerationID: "generation-next"}}, Compiler: release.CandidateCompilerEvidence{Graph: graph}}
	restoreOutcome := []identityledger.Outcome{
		{AuthoredID: "model_orders", Kind: projectgraph.KindModel, Outcome: identityledger.OutcomeRestoreRequired, Detail: "model is tombstoned"},
		{AuthoredID: "source_orders", Kind: projectgraph.KindSource, Outcome: identityledger.OutcomeRestoreRequired, Detail: "source is tombstoned"},
	}
	for _, test := range []struct {
		name       string
		restoreIDs []projectgraph.ResourceID
		wantErr    error
	}{
		{name: "missing tombstone", restoreIDs: []projectgraph.ResourceID{"source_orders"}, wantErr: identityledger.ErrRestoreRequired},
		{name: "extra identity", restoreIDs: []projectgraph.ResourceID{"model_orders", "source_orders", "unknown"}, wantErr: ErrIdentityLifecycleInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &identityLifecycleRepositoryFake{observed: "generation-current", outcomes: restoreOutcome}
			_, _, err := PlanIdentityCandidate(t.Context(), repository, IdentityCandidateInput{
				InstanceID: "instance-1", ExpectedBaseGenerationID: "generation-current", ActorID: "actor-1", Artifacts: artifacts,
				Restore: &deployment.RestoreIntent{AuthoredIDs: test.restoreIDs, Reason: "approved restore"},
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}

	repository := &identityLifecycleRepositoryFake{observed: "generation-current", outcomes: restoreOutcome}
	if _, _, err := PlanIdentityCandidate(t.Context(), repository, IdentityCandidateInput{
		InstanceID: "instance-1", ExpectedBaseGenerationID: "generation-current", ActorID: "actor-1", Artifacts: artifacts,
		Restore: &deployment.RestoreIntent{AuthoredIDs: []projectgraph.ResourceID{"source_orders", "model_orders"}, Reason: "approved restore"},
	}); err != nil {
		t.Fatalf("exact restore admission error = %v", err)
	}
}

func TestPrepareIdentityRestoreTransitionCarriesExactAuthorization(t *testing.T) {
	graph := identityLifecycleGraph(t)
	artifacts := release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{Identity: projectgraph.ServingIdentity{ProjectID: graph.ProjectID(), GenerationID: "generation-next"}}, Compiler: release.CandidateCompilerEvidence{Graph: graph}}
	events := []string{}
	repository := &identityLifecycleRepositoryFake{
		observed: "generation-current", events: &events,
		outcomes: []identityledger.Outcome{{AuthoredID: "source_orders", Kind: projectgraph.KindSource, Outcome: identityledger.OutcomeRestoreRequired, Detail: "source is tombstoned"}},
	}
	transition, err := PrepareIdentityRestoreTransition(t.Context(), repository, IdentityPublishPreparationInput{
		IdentityCandidateInput: IdentityCandidateInput{
			InstanceID: "instance-1", ExpectedBaseGenerationID: "generation-current", ActorID: "actor-1", Artifacts: artifacts,
			Restore: &deployment.RestoreIntent{AuthoredIDs: []projectgraph.ResourceID{"source_orders"}, Reason: "approved restore"},
		},
		CandidateID: "candidate-1", Reason: "ignored for restore",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Operation != identityledger.OperationRestore || transition.TransitionID != "identity-restore:candidate-1" || transition.Reason != "approved restore" || !reflect.DeepEqual(transition.ApprovedAuthoredIDs, []projectgraph.ResourceID{"source_orders"}) {
		t.Fatalf("restore transition = %#v", transition)
	}
	if !reflect.DeepEqual(events, []string{"plan", "prepare"}) {
		t.Fatalf("events = %#v, want plan, prepare", events)
	}
}

func TestProjectIdentityReferencesExcludesAuthoredGrantsAndRetainsPublicationTargets(t *testing.T) {
	resources := []projectgraph.Resource{{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"}}
	for _, item := range []struct {
		id   string
		kind projectgraph.Kind
	}{
		{"connection_orders", projectgraph.KindConnection}, {"source_orders", projectgraph.KindSource},
		{"model_orders", projectgraph.KindModel}, {"semantic_orders", projectgraph.KindSemanticModel},
		{"pipeline_orders", projectgraph.KindPipeline}, {"dashboard_orders", projectgraph.KindDashboard},
	} {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(item.id), Kind: item.kind, Name: item.id})
	}
	graph, err := projectgraph.NewProjectGraph(resources, nil)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := []string{"connection_orders", "source_orders", "model_orders", "semantic_orders", "pipeline_orders", "dashboard_orders"}
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{
		Graph: graph,
		Manifest: projectmanifest.Project{
			Access: projectmanifest.AccessPolicy{Grants: map[string]projectmanifest.Grant{
				"grant-model":   {ID: "grant-model", Object: projectmanifest.SecurableRef{ID: "model_orders", Kind: string(projectgraph.KindModel)}},
				"grant-project": {ID: "grant-project", Object: projectmanifest.SecurableRef{ID: "project_demo", Kind: string(projectgraph.KindProject)}},
			}},
			Publications: map[string]publication.Definition{
				"orders-public": {Name: "orders-public", DependencyAssetIDs: dependencies},
			},
		},
	}}
	references, err := ProjectIdentityReferences("instance-1", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 6 {
		t.Fatalf("reference count = %d, want six publication dependencies and no authored grants: %#v", len(references), references)
	}
	seen := make(map[projectgraph.Kind]bool)
	for _, reference := range references {
		if reference.OwnerKind != identityledger.ReferenceOwnerKindDashboardPublication {
			t.Fatalf("reference owner kind = %q, want dashboard publication: %#v", reference.OwnerKind, reference)
		}
		seen[reference.ExpectedKind] = true
	}
	for _, kind := range []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard} {
		if !seen[kind] {
			t.Errorf("no durable reference projected for %s", kind)
		}
	}
}

func TestProjectIdentityReferencesRejectsMissingAndOversizedBindings(t *testing.T) {
	graph := identityLifecycleGraph(t)
	missing := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Graph: graph, Manifest: projectmanifest.Project{Publications: map[string]publication.Definition{
		"public": {Name: "public", DependencyAssetIDs: []string{"missing"}},
	}}}}
	if _, err := ProjectIdentityReferences("instance-1", missing); err == nil {
		t.Fatal("missing publication dependency was accepted")
	}
	invalidOwner := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Graph: graph, Manifest: projectmanifest.Project{
		Access: projectmanifest.AccessPolicy{Grants: map[string]projectmanifest.Grant{
			"grant/bad": {ID: "grant/bad", Object: projectmanifest.SecurableRef{ID: "source_orders", Kind: string(projectgraph.KindSource)}},
		}},
	}}}
	if references, err := ProjectIdentityReferences("instance-1", invalidOwner); err != nil || len(references) != 0 {
		t.Fatalf("invalid authored grant was projected: references=%#v error=%v", references, err)
	}
	invalidPublication := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Graph: graph, Manifest: projectmanifest.Project{
		Publications: map[string]publication.Definition{
			"publication/bad": {Name: "publication/bad", DependencyAssetIDs: []string{"source_orders"}},
		},
	}}}
	if _, err := ProjectIdentityReferences("instance-1", invalidPublication); err == nil {
		t.Fatal("invalid publication authored identity was accepted")
	}

	longOwner := "p" + strings.Repeat("o", 127)
	longTarget := "s" + strings.Repeat("t", 127)
	longGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: projectgraph.ResourceID(longTarget), Kind: projectgraph.KindSource, Name: "long-source"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	oversized := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Graph: longGraph, Manifest: projectmanifest.Project{Publications: map[string]publication.Definition{
		longOwner: {Name: longOwner, DependencyAssetIDs: []string{longTarget}},
	}}}}
	if _, err := ProjectIdentityReferences("instance-1", oversized); err == nil {
		t.Fatal("oversized transparent reference identity was accepted")
	}
}

func TestIdentitySealedCoordinatorPublishesAfterSealedPreflightBeforeTargetCommit(t *testing.T) {
	graph := identityLifecycleGraph(t)
	events := []string{}
	published := identityledger.Transition{TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish, InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next", ExpectedBundleID: "generation-current", ActorID: "actor-1", GraphDigest: graph.Digest(), Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}}, References: []identityledger.DurableReference{
		{InstanceID: "instance-1", ReferenceID: "grant:reader", OwnerAuthoredID: "reader", OwnerKind: identityledger.ReferenceOwnerKindGrant, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
		{InstanceID: "instance-1", ReferenceID: "dashboard_publication:5:share:13:source_orders", OwnerAuthoredID: "share", OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
	}}
	repository := &identityLifecycleRepositoryFake{published: published, observed: "generation-current", events: &events}
	sealed := &identityLifecycleSealedFake{publishResult: deployment.PublicationIntent{ID: "publication-1"}, events: &events}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	got, err := coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "publication-1" || sealed.publishCalls != 1 {
		t.Fatalf("publication = %#v, sealed calls = %d", got, sealed.publishCalls)
	}
	if indexOfIdentityEvent(events, "sealed-preflight") > indexOfIdentityEvent(events, "activate") || indexOfIdentityEvent(events, "activate") > indexOfIdentityEvent(events, "references") || indexOfIdentityEvent(events, "references") > indexOfIdentityEvent(events, "sealed-commit") {
		t.Fatalf("identity activation was not nested after sealed preflight and before target commit: %#v", events)
	}
	if len(repository.references) != 1 || repository.references[0].OwnerKind != identityledger.ReferenceOwnerKindDashboardPublication {
		t.Fatalf("reconciled references = %#v, want only publication reference", repository.references)
	}
	if indexOfIdentityEvent(events, "references") > indexOfIdentityEvent(events, "sealed-publish") {
		t.Fatalf("sealed publication ran before durable references: %#v", events)
	}
}

func TestIdentitySealedCoordinatorRestoreApprovalPrecedesIdentityMutation(t *testing.T) {
	graph := identityLifecycleGraph(t)
	restore := identityledger.Transition{
		TransitionID: "identity-restore:candidate-1", Operation: identityledger.OperationRestore,
		InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next",
		ExpectedBundleID: "generation-current", ActorID: "candidate-owner", Reason: "approved restore",
		ApprovedAuthoredIDs: []projectgraph.ResourceID{"source_orders"}, GraphDigest: graph.Digest(),
		Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}},
		Phase:     identityledger.PhasePrepared,
	}
	repository := &identityLifecycleRepositoryFake{
		transition: restore, published: restore, observed: "generation-current",
		outcomes: []identityledger.Outcome{{AuthoredID: "source_orders", Kind: projectgraph.KindSource, Outcome: identityledger.OutcomeRestoreRequired}},
	}
	sealed := &identityLifecycleSealedFake{publishErr: errors.New("approval required")}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current")); !errors.Is(err, sealed.publishErr) {
		t.Fatalf("missing approval error = %v, want %v", err, sealed.publishErr)
	}
	if repository.restoreCalls != 0 {
		t.Fatalf("restore calls = %d, want no identity mutation before approval", repository.restoreCalls)
	}
}

func TestIdentitySealedCoordinatorApprovedRestoreCommitsOnceAndReplays(t *testing.T) {
	graph := identityLifecycleGraph(t)
	restore := identityledger.Transition{
		TransitionID: "identity-restore:candidate-1", Operation: identityledger.OperationRestore,
		InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next",
		ExpectedBundleID: "generation-current", ActorID: "candidate-owner", Reason: "approved restore",
		ApprovedAuthoredIDs: []projectgraph.ResourceID{"source_orders"}, GraphDigest: graph.Digest(),
		Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}},
		Phase:     identityledger.PhasePrepared,
	}
	repository := &identityLifecycleRepositoryFake{
		transition: restore, published: restore, observed: "generation-current",
		outcomes: []identityledger.Outcome{{AuthoredID: "source_orders", Kind: projectgraph.KindSource, Outcome: identityledger.OutcomeRestoreRequired}},
	}
	sealed := &identityLifecycleSealedFake{publishResult: deployment.PublicationIntent{ID: "publication-1"}}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	runtimeActivations := 0
	activate := func(ctx context.Context, commit func() error) error {
		runtimeActivations++
		return commit()
	}
	if _, err := coordinator.PublishWithActivation(t.Context(), identityLifecyclePublishRequest("generation-current"), activate); err != nil {
		t.Fatal(err)
	}
	if repository.restoreCalls != 1 || !reflect.DeepEqual(repository.restoredIDs, []projectgraph.ResourceID{"source_orders"}) || sealed.publishCommitCalls != 1 || runtimeActivations != 1 {
		t.Fatalf("first restore calls: restore=%d ids=%#v target=%d runtime=%d", repository.restoreCalls, repository.restoredIDs, sealed.publishCommitCalls, runtimeActivations)
	}
	// A restart/retry sees the completed identity transition. It must rerun
	// runtime reconciliation against sealedcontrol's no-op committed callback,
	// without invoking RestoreAndActivate a second time.
	runtimeActivations = 0
	if _, err := coordinator.PublishWithActivation(t.Context(), identityLifecyclePublishRequest("generation-current"), activate); err != nil {
		t.Fatal(err)
	}
	if repository.restoreCalls != 1 || sealed.publishCommitCalls != 1 || runtimeActivations != 1 {
		t.Fatalf("replay calls: restore=%d target=%d runtime=%d", repository.restoreCalls, sealed.publishCommitCalls, runtimeActivations)
	}
}

func TestIdentitySealedCoordinatorOrdinaryPublishStillRejectsTombstones(t *testing.T) {
	graph := identityLifecycleGraph(t)
	published := identityledger.Transition{
		TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish,
		InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next",
		ExpectedBundleID: "generation-current", ActorID: "candidate-owner", GraphDigest: graph.Digest(),
		Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}}, Phase: identityledger.PhasePrepared,
	}
	repository := &identityLifecycleRepositoryFake{
		transition: published, published: published, observed: "generation-current",
		outcomes: []identityledger.Outcome{{AuthoredID: "source_orders", Kind: projectgraph.KindSource, Outcome: identityledger.OutcomeRestoreRequired}},
	}
	sealed := &identityLifecycleSealedFake{publishResult: deployment.PublicationIntent{ID: "publication-1"}}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current")); !errors.Is(err, identityledger.ErrRestoreRequired) {
		t.Fatalf("ordinary publish error = %v, want restore required", err)
	}
	if sealed.publishCommitCalls != 0 || repository.restoreCalls != 0 {
		t.Fatalf("ordinary publish mutated target/identity: target=%d restore=%d", sealed.publishCommitCalls, repository.restoreCalls)
	}
}

func TestIdentitySealedCoordinatorPreservesCandidateOwnerAndPublicationActorBoundaries(t *testing.T) {
	graph := identityLifecycleGraph(t)
	published := identityledger.Transition{
		TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish,
		InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next",
		ExpectedBundleID: "generation-current", ActorID: "candidate-owner", GraphDigest: graph.Digest(),
		Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}},
	}
	repository := &identityLifecycleRepositoryFake{published: published, observed: "generation-current"}
	sealed := &identityLifecycleSealedFake{publishResult: deployment.PublicationIntent{ID: "publication-1"}}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	request := identityLifecyclePublishRequest("generation-current")
	request.ActorID = "authorized-reviewer"
	if _, err := coordinator.Publish(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if repository.activatedActor != "candidate-owner" {
		t.Fatalf("identity actor = %q, want immutable candidate owner", repository.activatedActor)
	}
	if sealed.publishActor != request.ActorID {
		t.Fatalf("sealed publication actor = %q, want authorized command actor %q", sealed.publishActor, request.ActorID)
	}
}

func TestIdentitySealedCoordinatorReconcilesReferenceSetAndRetries(t *testing.T) {
	graph := identityLifecycleGraph(t)
	desired := []identityledger.DurableReference{
		{InstanceID: "instance-1", ReferenceID: "grant:reader", OwnerAuthoredID: "reader", OwnerKind: identityledger.ReferenceOwnerKindGrant, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
		{InstanceID: "instance-1", ReferenceID: "dashboard_publication:5:share:13:source_orders", OwnerAuthoredID: "share", OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
	}
	normalizedDesired, err := identityledger.NormalizeReferences("instance-1", desired[1:])
	if err != nil {
		t.Fatal(err)
	}
	published := identityledger.Transition{TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish, InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next", ExpectedBundleID: "generation-current", ActorID: "actor-1", GraphDigest: graph.Digest(), Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}}, References: desired}
	repository := &identityLifecycleRepositoryFake{published: published, observed: "generation-current", referenceErr: errors.New("reference reconciliation unavailable")}
	sealed := &identityLifecycleSealedFake{publishResult: deployment.PublicationIntent{ID: "publication-1"}}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current")); !errors.Is(err, repository.referenceErr) {
		t.Fatalf("first reference reconciliation error = %v", err)
	}
	if sealed.publishCalls != 0 || repository.transition.Phase != identityledger.PhaseIdentityActive {
		t.Fatalf("first reconciliation failure published delivery: sealed=%d phase=%q", sealed.publishCalls, repository.transition.Phase)
	}
	repository.referenceErr = nil
	if _, err := coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current")); err != nil {
		t.Fatal(err)
	}
	if sealed.publishCalls != 1 || len(repository.references) != len(normalizedDesired) {
		t.Fatalf("retry reconciliation calls = %d, references = %#v", sealed.publishCalls, repository.references)
	}
	for index := range normalizedDesired {
		if repository.references[index] != normalizedDesired[index] {
			t.Fatalf("reconciled reference[%d] = %#v, want %#v", index, repository.references[index], normalizedDesired[index])
		}
	}
}

func TestIdentitySealedCoordinatorRollbackReusesPublishedEvidence(t *testing.T) {
	graph := identityLifecycleGraph(t)
	published := identityledger.Transition{TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish, InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-old", ExpectedBundleID: "generation-base", ActorID: "actor-publish", GraphDigest: graph.Digest(), Phase: identityledger.PhaseCompleted, Resources: []identityledger.Resource{{AuthoredID: "model_orders", Kind: projectgraph.KindModel}, {AuthoredID: "source_orders", Kind: projectgraph.KindSource}}, References: []identityledger.DurableReference{
		{InstanceID: "instance-1", ReferenceID: "grant:revoked", OwnerAuthoredID: "revoked", OwnerKind: identityledger.ReferenceOwnerKindGrant, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
		{InstanceID: "instance-1", ReferenceID: "dashboard_publication:5:share:13:source_orders", OwnerAuthoredID: "share", OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
	}}
	repository := &identityLifecycleRepositoryFake{published: published, observed: "generation-current"}
	sealed := &identityLifecycleSealedFake{rollbackResult: deployment.RollbackResult{RequestDigest: "result-1"}}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	request := identityLifecycleRollbackRequest("generation-current", "generation-old")
	got, err := coordinator.Rollback(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestDigest != "result-1" || sealed.rollbackCalls != 1 {
		t.Fatalf("rollback = %#v, sealed calls = %d", got, sealed.rollbackCalls)
	}
	if repository.transition.GraphDigest != published.GraphDigest || !reflect.DeepEqual(repository.transition.Resources, published.Resources) {
		t.Fatalf("rollback transition evidence = %#v, want graph=%q resources=%#v", repository.transition, published.GraphDigest, published.Resources)
	}
	if repository.transition.TransitionID != "identity-rollback:rollback-1" {
		t.Fatalf("rollback transition ID = %q", repository.transition.TransitionID)
	}
	if len(repository.references) != 1 || repository.references[0].ReferenceID != "dashboard_publication:5:share:13:source_orders" {
		t.Fatalf("rollback reconciled references = %#v, want only publication reference", repository.references)
	}
}

func TestIdentityRollbackTransitionRequiresCompletedPublishEvidence(t *testing.T) {
	graph := identityLifecycleGraph(t)
	request := identityLifecycleRollbackRequest("generation-current", "generation-old")
	for _, phase := range []identityledger.TransitionPhase{identityledger.PhasePrepared, identityledger.PhaseIdentityActive, identityledger.PhaseDeliveryActive} {
		published := identityledger.Transition{
			Operation: identityledger.OperationPublish, InstanceID: "instance-1", CandidateID: "candidate-1",
			BundleID: "generation-old", GraphDigest: graph.Digest(), Phase: phase,
		}
		if _, err := IdentityRollbackTransition(request, published); !errors.Is(err, identityledger.ErrTransitionConflict) {
			t.Fatalf("phase %q error = %v, want ErrTransitionConflict", phase, err)
		}
	}
}

func TestIdentityRollbackTransitionAcceptsCompletedRestoreEvidence(t *testing.T) {
	published := identityledger.Transition{
		TransitionID: "identity-restore:candidate-1", Operation: identityledger.OperationRestore,
		InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-old",
		ExpectedBundleID: "generation-current", ActorID: "candidate-owner", Reason: "reviewed restore",
		ApprovedAuthoredIDs: []projectgraph.ResourceID{"source_orders"},
		Resources:           []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}},
		GraphDigest:         identityLifecycleGraph(t).Digest(), Phase: identityledger.PhaseCompleted,
	}
	request := identityLifecycleRollbackRequest("generation-current", published.BundleID)
	transition, err := IdentityRollbackTransition(request, published)
	if err != nil {
		t.Fatal(err)
	}
	if transition.Operation != identityledger.OperationRollback || transition.BundleID != published.BundleID || transition.GraphDigest != published.GraphDigest {
		t.Fatalf("rollback transition = %#v", transition)
	}
}

func TestIdentitySealedCoordinatorFailsClosedWhenEvidenceOrDeliveryUnavailable(t *testing.T) {
	if _, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Sealed: &identityLifecycleSealedFake{}}); !errors.Is(err, ErrIdentityLifecycleUnavailable) {
		t.Fatalf("missing repository error = %v", err)
	}
	graph := identityLifecycleGraph(t)
	repository := &identityLifecycleRepositoryFake{published: identityledger.Transition{Operation: identityledger.OperationPublish, InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next", GraphDigest: graph.Digest()}}
	if _, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: identityLifecycleBasicSealedFake{}}); !errors.Is(err, ErrIdentityLifecycleUnavailable) {
		t.Fatalf("missing sealed activation callback error = %v", err)
	}
	sealed := &identityLifecycleSealedFake{}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	badRequest := identityLifecyclePublishRequest("generation-current")
	badRequest.Generation.CandidateID = "candidate-other"
	if _, err := coordinator.Publish(t.Context(), badRequest); err == nil {
		t.Fatal("missing rollback/publish evidence unexpectedly published")
	}
	if sealed.publishCalls != 0 {
		t.Fatal("sealed publication ran without complete identity evidence")
	}

	repository.published = identityledger.Transition{TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish, InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next", ExpectedBundleID: "generation-current", ActorID: "actor-1", GraphDigest: graph.Digest(), Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}}}
	sealed.publishErr = errors.New("sealed unavailable")
	repository.observed = "generation-current"
	_, err = coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current"))
	if !errors.Is(err, sealed.publishErr) || sealed.publishCalls != 1 {
		t.Fatalf("delivery failure = %v, sealed calls = %d", err, sealed.publishCalls)
	}

	sealed.publishErr = nil
	sealed.publishCalls = 0
	repository.transition = identityledger.Transition{}
	repository.observed = "generation-current"
	repository.published.References = []identityledger.DurableReference{
		{InstanceID: "instance-1", ReferenceID: "grant:reader", OwnerAuthoredID: "reader", OwnerKind: identityledger.ReferenceOwnerKindGrant, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
		{InstanceID: "instance-1", ReferenceID: "dashboard_publication:5:share:13:source_orders", OwnerAuthoredID: "share", OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication, TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
	}
	repository.referenceErr = errors.New("reference unavailable")
	_, err = coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current"))
	if !errors.Is(err, repository.referenceErr) || sealed.publishCalls != 0 {
		t.Fatalf("reference failure = %v, sealed calls = %d", err, sealed.publishCalls)
	}
}

func TestIdentitySealedCoordinatorRejectsReferenceWriterBindingDrift(t *testing.T) {
	graph := identityLifecycleGraph(t)
	published := identityledger.Transition{
		TransitionID: "identity-publish:candidate-1", Operation: identityledger.OperationPublish,
		InstanceID: "instance-1", CandidateID: "candidate-1", BundleID: "generation-next",
		ExpectedBundleID: "generation-current", ActorID: "actor-1", GraphDigest: graph.Digest(),
		Resources: []identityledger.Resource{{AuthoredID: "source_orders", Kind: projectgraph.KindSource}},
		References: []identityledger.DurableReference{
			{InstanceID: "instance-1", ReferenceID: "grant:reader", OwnerAuthoredID: "reader", OwnerKind: identityledger.ReferenceOwnerKindGrant,
				TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
			{InstanceID: "instance-1", ReferenceID: "dashboard_publication:5:share:13:source_orders", OwnerAuthoredID: "share", OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication,
				TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource},
		},
	}
	repository := &identityLifecycleRepositoryFake{
		published: published, observed: "generation-current",
		referenceResult: identityledger.DurableReference{
			InstanceID: "instance-1", ReferenceID: "dashboard_publication:5:share:13:source_orders", OwnerAuthoredID: "other", OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication,
			TargetAuthoredID: "source_orders", ExpectedKind: projectgraph.KindSource,
		},
	}
	sealed := &identityLifecycleSealedFake{publishResult: deployment.PublicationIntent{ID: "publication-1"}}
	coordinator, err := NewIdentitySealedCoordinator(IdentitySealedCoordinatorConfig{InstanceID: "instance-1", Transitions: repository, Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(t.Context(), identityLifecyclePublishRequest("generation-current")); !errors.Is(err, ErrIdentityLifecycleInvalid) {
		t.Fatalf("binding drift error = %v, want ErrIdentityLifecycleInvalid", err)
	}
	if sealed.publishCalls != 0 {
		t.Fatal("sealed publication ran after reference writer binding drift")
	}
}

func identityLifecycleGraph(t *testing.T) projectgraph.ProjectGraph {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "source_orders", Kind: projectgraph.KindSource, Name: "orders_source"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders_model"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func indexOfIdentityEvent(events []string, wanted string) int {
	for index, event := range events {
		if event == wanted {
			return index
		}
	}
	return len(events) + 1
}

func identityLifecycleDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func identityLifecycleSeal() deployment.VerifiedSeal {
	return deployment.VerifiedSeal{
		SealID: "seal-1", CatalogDigest: identityLifecycleDigest('a'),
		CatalogObjectKey: "catalogs/sha256/" + strings.Repeat("a", 64) + ".ducklake",
		ObjectSize:       1, PhysicalPoolID: "pool-1", CompatibilityDigest: identityLifecycleDigest('b'),
		ClosureDigest: identityLifecycleDigest('c'), QualificationDigest: identityLifecycleDigest('d'),
		ServingArtifactID: "artifact-1", ServingArtifactDigest: identityLifecycleDigest('e'),
	}
}

func identityLifecyclePublishRequest(expectedBase string) sealedcontrol.PublishRequest {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	seal := identityLifecycleSeal()
	generation, _ := deployment.NewCatalogRoot(deployment.CatalogRoot{
		ID: "generation-next", CandidateID: "candidate-1", PlanID: "plan-1", PlanDigest: identityLifecycleDigest('f'),
		TargetID: "target-1", ProjectID: "project_demo", Environment: "prod",
		CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.CatalogObjectKey, PhysicalPoolID: seal.PhysicalPoolID,
		ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest,
		ServingStateID: "generation-next", CompatibilityDigest: seal.CompatibilityDigest,
		RollbackClass: deployment.DeliveryRollbackSafe, CreatedAt: now,
	})
	publication, _ := deployment.NewPublicationIntent(deployment.PublicationIntent{
		ID: "publication-1", RequestDigest: identityLifecycleDigest('9'), TargetID: generation.TargetID,
		ProjectID: generation.ProjectID, Environment: generation.Environment, PlanID: generation.PlanID,
		PlanDigest: generation.PlanDigest, CandidateID: generation.CandidateID, GenerationID: generation.ID,
		ExpectedBaseGenerationID: expectedBase, ExpectedTargetRevision: 1, CreatedAt: now,
	})
	return sealedcontrol.PublishRequest{Publication: publication, Generation: generation, Seal: seal, ActorID: "actor-1"}
}

func identityLifecycleRollbackRequest(expectedBase, generationID string) sealedcontrol.RollbackRequest {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	return sealedcontrol.RollbackRequest{ActorID: "actor-rollback", Request: deployment.RollbackRequest{
		ID: "rollback-1", RequestDigest: identityLifecycleDigest('8'), TargetID: "target-1",
		ProjectID: "project_demo", Environment: "prod", CandidateID: "candidate-1", GenerationID: generationID,
		ExpectedBaseGenerationID: expectedBase, ExpectedTargetRevision: 2, VerifiedSeal: identityLifecycleSeal(), CreatedAt: now,
	}}
}
