package validate

import (
	"context"
	"errors"
	"reflect"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestServiceValidatesPromotesAndSaves(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{
		deployment: servingstate.State{ID: "dep_1", ProjectID: "project", Status: servingstate.StatusPending},
	}
	artifacts := &fakeArtifacts{}
	validator := &fakeValidator{
		validation: servingstate.Validation{Digest: "digest", ManifestJSON: `{"version":1}`, RootDir: "/tmp/root"},
	}
	service := NewService(repo, artifacts, validator)

	validated, err := service.Validate(ctx, "dep_1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if validated.Status != servingstate.StatusValidated {
		t.Fatalf("status = %q, want validated", validated.Status)
	}
	if artifacts.promotedDigest != "digest" || !repo.saved {
		t.Fatalf("promoted digest=%q saved=%v, want digest/true", artifacts.promotedDigest, repo.saved)
	}
	if !validator.cleaned {
		t.Fatal("validator cleanup was not called")
	}
}

func TestServiceResumesAlreadyValidatedCandidateWithoutReprocessingArtifact(t *testing.T) {
	repo := &fakeRepo{
		deployment: servingstate.State{
			ID:            "dep_1",
			ProjectID:     "project",
			ProjectDigest: "sha256:project",
			Status:        servingstate.StatusValidated,
			Digest:        "sha256:artifact",
		},
	}
	artifacts := &fakeArtifacts{}
	validator := &fakeValidator{err: errors.New("validated artifact must not be reprocessed")}
	service := NewService(repo, artifacts, validator)

	resumed, err := service.Validate(t.Context(), "dep_1")
	if err != nil {
		t.Fatalf("Validate() resumed candidate error = %v", err)
	}
	if !reflect.DeepEqual(resumed, repo.deployment) {
		t.Fatalf("Validate() = %#v, want persisted candidate %#v", resumed, repo.deployment)
	}
	if artifacts.promoteCalls != 0 || validator.cleaned || repo.saved {
		t.Fatalf("resume reprocessed artifact: promote=%d cleaned=%v saved=%v", artifacts.promoteCalls, validator.cleaned, repo.saved)
	}
}

func TestServiceMarksFailedWhenValidationFails(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{
		deployment: servingstate.State{ID: "dep_1", ProjectID: "project", Status: servingstate.StatusPending},
	}
	validator := &fakeValidator{err: errors.New("bad bundle")}
	service := NewService(repo, &fakeArtifacts{}, validator)

	if _, err := service.Validate(ctx, "dep_1"); err == nil {
		t.Fatal("expected validation error")
	}
	if !repo.failed {
		t.Fatal("deployment was not marked failed")
	}
	if repo.saved {
		t.Fatal("invalid deployment was saved")
	}
}

func TestServiceRunsHookAfterArtifactValidationBeforePromotion(t *testing.T) {
	events := []string{}
	repo := &fakeRepo{
		deployment: servingstate.State{ID: "dep_1", ProjectID: "project", Environment: "prod", Status: servingstate.StatusPending},
		events:     &events,
	}
	artifacts := &fakeArtifacts{events: &events}
	validator := &fakeValidator{
		validation: servingstate.Validation{Digest: "digest", ManifestJSON: `{}`, RootDir: "/tmp/extracted"},
		events:     &events,
	}
	hook := &fakeHook{events: &events}
	service := NewService(repo, artifacts, validator, hook)

	if _, err := service.Validate(t.Context(), "dep_1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"validated", "hook", "promoted", "saved", "discarded", "cleaned"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if hook.candidate.ID != "dep_1" || hook.candidate.Environment != "prod" || hook.validation.RootDir != "/tmp/extracted" {
		t.Fatalf("hook candidate = %#v validation = %#v", hook.candidate, hook.validation)
	}
}

func TestServiceSuppliesManagedDataRevisionsToValidationHooks(t *testing.T) {
	repo := &fakeRepo{
		deployment: servingstate.State{ID: "dep_1", ProjectID: "project", Environment: "prod", Status: servingstate.StatusPending},
	}
	validator := &fakeValidator{
		validation: servingstate.Validation{Digest: "digest", ManifestJSON: `{}`, RootDir: "/tmp/extracted"},
	}
	hook := &fakeHook{}
	service := NewService(repo, &fakeArtifacts{}, validator, hook)
	revisions := map[string]string{"connection:orders": "sha256:revision"}

	if _, err := service.ValidateWithManagedDataRevisions(t.Context(), "dep_1", revisions); err != nil {
		t.Fatalf("ValidateWithManagedDataRevisions() error = %v", err)
	}
	if !reflect.DeepEqual(hook.validation.ManagedDataRevisions, revisions) {
		t.Fatalf("managed data revisions = %#v, want %#v", hook.validation.ManagedDataRevisions, revisions)
	}
	revisions["connection:orders"] = "sha256:mutated"
	if got := hook.validation.ManagedDataRevisions["connection:orders"]; got != "sha256:revision" {
		t.Fatalf("hook revision changed with caller map: got %q", got)
	}
}

func TestServiceRejectsMissingManagedDataRevisions(t *testing.T) {
	service := NewService(&fakeRepo{}, &fakeArtifacts{}, &fakeValidator{})

	if _, err := service.ValidateWithManagedDataRevisions(t.Context(), "dep_1", nil); err == nil {
		t.Fatal("ValidateWithManagedDataRevisions() error = nil, want missing revisions error")
	}
}

func TestServiceMarksFailedAndDoesNotPromoteWhenHookFails(t *testing.T) {
	hookErr := errors.New("managed data current revision is unavailable")
	repo := &fakeRepo{
		deployment: servingstate.State{ID: "dep_1", ProjectID: "project", Environment: "prod", Status: servingstate.StatusPending},
	}
	artifacts := &fakeArtifacts{}
	validator := &fakeValidator{validation: servingstate.Validation{RootDir: "/tmp/extracted"}}
	service := NewService(repo, artifacts, validator, &fakeHook{err: hookErr})

	_, err := service.Validate(t.Context(), "dep_1")
	if !errors.Is(err, hookErr) {
		t.Fatalf("Validate() error = %v, want hook error", err)
	}
	if !repo.failed {
		t.Fatal("publish was not marked failed")
	}
	if artifacts.promoteCalls != 0 || repo.saved {
		t.Fatalf("promote calls = %d, saved = %v; want neither", artifacts.promoteCalls, repo.saved)
	}
	if !validator.cleaned {
		t.Fatal("validator cleanup was not called")
	}
}

type fakeRepo struct {
	deployment servingstate.State
	saved      bool
	failed     bool
	events     *[]string
}

func (r *fakeRepo) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return r.deployment, nil
}

func (r *fakeRepo) MarkFailed(context.Context, servingstate.ID, error) error {
	r.failed = true
	return nil
}

func (r *fakeRepo) SaveValidated(_ context.Context, _ servingstate.ID, validation servingstate.Validation, artifact servingstate.Artifact) (servingstate.State, error) {
	appendEvent(r.events, "saved")
	r.saved = true
	r.deployment.Status = servingstate.StatusValidated
	r.deployment.Digest = validation.Digest
	r.deployment.ManifestJSON = artifact.ManifestJSON
	return r.deployment, nil
}

type fakeArtifacts struct {
	promotedDigest string
	promoteCalls   int
	discardCalls   int
	events         *[]string
}

func (a *fakeArtifacts) UploadPath(servingstate.ID) string {
	return "upload.tar.gz"
}

func (a *fakeArtifacts) PromoteUploaded(_ context.Context, servingStateID servingstate.ID, digest, manifestJSON string) (servingstate.Artifact, error) {
	a.promoteCalls++
	appendEvent(a.events, "promoted")
	a.promotedDigest = digest
	return servingstate.Artifact{ID: "artifact_" + string(servingStateID), ServingStateID: servingStateID, Digest: digest, ManifestJSON: manifestJSON}, nil
}

func (a *fakeArtifacts) DiscardUploaded(context.Context, servingstate.ID) error {
	a.discardCalls++
	appendEvent(a.events, "discarded")
	return nil
}

type fakeValidator struct {
	validation  servingstate.Validation
	err         error
	cleaned     bool
	environment servingstate.Environment
	events      *[]string
}

func (v *fakeValidator) ValidateArtifact(_ string, _ projectgraph.ResourceID, environment servingstate.Environment, _ servingstate.ID) (servingstate.Validation, error) {
	v.environment = environment
	if v.err != nil {
		return servingstate.Validation{}, v.err
	}
	appendEvent(v.events, "validated")
	return v.validation, nil
}

func (v *fakeValidator) Cleanup(servingstate.Validation) error {
	v.cleaned = true
	appendEvent(v.events, "cleaned")
	return nil
}

type fakeHook struct {
	err        error
	candidate  servingstate.State
	validation servingstate.Validation
	events     *[]string
}

func (h *fakeHook) AfterArtifactValidation(_ context.Context, candidate servingstate.State, validation servingstate.Validation) error {
	h.candidate = candidate
	h.validation = validation
	appendEvent(h.events, "hook")
	return h.err
}

func appendEvent(events *[]string, event string) {
	if events != nil {
		*events = append(*events, event)
	}
}
