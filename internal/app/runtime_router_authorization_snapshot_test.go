package app

import (
	"context"
	"testing"
)

func TestAuthorizationSnapshotFromProviderUsesProvidedRuntime(t *testing.T) {
	want := tusSnapshot(t, "candidate_owner", "connection_sample", true)
	provider := tusRuntime{project: "project_demo", lease: tusLease{identity: want.Identity(), snapshot: want}}

	resolve := authorizationSnapshotFromProvider(provider)
	if resolve == nil {
		t.Fatal("authorization snapshot resolver is nil")
	}
	got, err := resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity() != want.Identity() {
		t.Fatalf("authorization snapshot identity = %#v, want %#v", got.Identity(), want.Identity())
	}
}

func TestAuthorizationSnapshotFromProviderRejectsNilProvider(t *testing.T) {
	if resolve := authorizationSnapshotFromProvider(nil); resolve != nil {
		t.Fatal("nil provider produced an authorization snapshot resolver")
	}
}
