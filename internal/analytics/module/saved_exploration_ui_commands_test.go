package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
)

func TestSavedExplorationUICommandAdapterRunsGeneratedCASCommand(t *testing.T) {
	invocation := SavedExplorationUICommandInvocation{
		Action: "update", Project: "project:test", Resource: "exploration:test",
		IdempotencyKey: "ui:saved-update-1", RequestID: "saved-update-1", CorrelationID: "saved-update-1",
		Revision: saved.RevisionToken{RevisionID: "revision:test", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)},
	}
	attested := invocation.Revision
	invocation.ConcurrencyRevision = &attested
	started, err := (*Module)(nil).BeginSavedExplorationUICommand(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	if err := (*Module)(nil).ExecuteSavedExplorationUICommand(started, invocation, func(context.Context) error {
		called++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("saved mutation callback count = %d, want 1", called)
	}
}

func TestSavedExplorationUICommandAttestationComparesIndependentRevision(t *testing.T) {
	presented := saved.RevisionToken{RevisionID: "revision:test", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	for _, test := range []struct {
		name    string
		current saved.RevisionToken
		wantErr error
	}{
		{name: "matching", current: presented},
		{name: "stale mismatch", current: saved.RevisionToken{RevisionID: presented.RevisionID, Number: 2, ContentHash: presented.ContentHash}, wantErr: apigencommand.ErrPreconditionFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation := SavedExplorationUICommandInvocation{
				Action: "update", Project: "project:test", Resource: "exploration:test",
				IdempotencyKey: "ui:attestation-" + test.name, RequestID: "attestation-" + test.name, CorrelationID: "attestation-" + test.name,
				Revision: presented,
			}
			current := test.current
			invocation.ConcurrencyRevision = &current
			started, err := (*Module)(nil).BeginSavedExplorationUICommand(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			called := 0
			err = (*Module)(nil).ExecuteSavedExplorationUICommand(started, invocation, func(context.Context) error {
				called++
				return nil
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("attestation error = %v, want %v", err, test.wantErr)
			}
			if called != 1 {
				t.Fatalf("mutation callback count = %d, want 1", called)
			}
		})
	}
}

func TestSavedExplorationUICommandAdapterRequiresExactMutationRevision(t *testing.T) {
	invocation := SavedExplorationUICommandInvocation{Action: "archive", Project: "project:test", IdempotencyKey: "ui:saved-archive-1", RequestID: "saved-archive-1"}
	if _, err := (*Module)(nil).BeginSavedExplorationUICommand(context.Background(), invocation); !errors.Is(err, saved.ErrInvalidRevision) {
		t.Fatalf("missing archive revision error = %v, want invalid revision", err)
	}
	called := false
	if err := (*Module)(nil).ExecuteSavedExplorationUICommand(context.Background(), invocation, func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, saved.ErrInvalidRevision) {
		t.Fatalf("missing archive revision execute error = %v, want invalid revision", err)
	}
	if called {
		t.Fatal("invalid archive revision invoked mutation callback")
	}
}
