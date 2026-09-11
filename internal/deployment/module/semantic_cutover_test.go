package module

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/pkg/jobs"
)

func TestSemanticCutoverFenceRunsBeforeActivationCommit(t *testing.T) {
	row := apiadapter.Deployment{ID: "deployment_1", Project: "project_demo", Environment: "prod", GenerationID: "generation_candidate", Status: apiadapter.StatusPending}
	payload, err := json.Marshal(ActivateJob{Project: row.Project, Deployment: row.ID, Actor: "publisher", IdempotencyKey: "activate-1"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rejection leaves active state unchanged", func(t *testing.T) {
		coordinator := &activationMutationCoordinatorStub{row: row}
		wantErr := errors.New("semantic activation evidence is stale")
		module := &Module{jobs: JobConfig{Coordinator: coordinator, ValidateCutover: func(_ context.Context, input ActivationCutoverInput) error {
			if input.GenerationID != row.GenerationID || input.Actor != "publisher" || input.Rollback {
				t.Fatalf("cutover input = %#v", input)
			}
			return wantErr
		}}}
		if err := module.activate(t.Context(), jobs.Job{Payload: payload}); !errors.Is(err, wantErr) {
			t.Fatalf("activation error = %v, want %v", err, wantErr)
		}
		if coordinator.activateCalls != 0 || coordinator.row.Status != apiadapter.StatusPending {
			t.Fatalf("rejected cutover mutated activation: calls=%d status=%q", coordinator.activateCalls, coordinator.row.Status)
		}
	})

	t.Run("valid rollback is explicitly identified", func(t *testing.T) {
		coordinator := &activationMutationCoordinatorStub{row: row}
		rollbackPayload, err := json.Marshal(ActivateJob{Project: row.Project, Deployment: row.ID, Actor: "publisher", IdempotencyKey: "rollback-1", Rollback: true})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		module := &Module{jobs: JobConfig{Coordinator: coordinator, ValidateCutover: func(_ context.Context, input ActivationCutoverInput) error {
			calls++
			if !input.Rollback {
				t.Fatal("rollback activation was not identified to the final fence")
			}
			return nil
		}}}
		if err := module.activate(t.Context(), jobs.Job{Payload: rollbackPayload}); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || coordinator.activateCalls != 1 || coordinator.row.Status != apiadapter.StatusActive {
			t.Fatalf("rollback: fence=%d activation=%d status=%q", calls, coordinator.activateCalls, coordinator.row.Status)
		}
	})
}

func TestNativeActivationWorkflowPreservesRollbackIdentity(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		intent, err := nativePublicationActivationWorkflow("project_demo", "prod", "publisher", rollback)("publication-1")
		if err != nil {
			t.Fatal(err)
		}
		var payload ActivateJob
		if err := json.Unmarshal(intent.Job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Rollback != rollback {
			t.Fatalf("rollback=%t workflow payload = %#v", rollback, payload)
		}
	}
}
