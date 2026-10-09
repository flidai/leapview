package connectionbinding

import (
	"context"
	"testing"
	"time"
)

func TestCredentialRetirementClosesOldPoolAndAllowsNewGeneration(t *testing.T) {
	binding := validTargetBinding(t)
	factory := &recordingPoolFactory{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{Build: func(current TargetBinding) (*PoolManager, error) {
		return NewPoolManager(PoolManagerConfig{Binding: current, Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", current.UpdatedAt)}}, Factory: factory, Store: &recordingBindingStore{}, Audit: noOpRotationAudit{}, Now: func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour})
	}, RefreshTimeout: time.Second, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	old, err := directory.Pool(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.Refresh(context.Background(), RefreshRequest{Actor: "operator", Operation: RefreshRequested}); err != nil {
		t.Fatal(err)
	}
	if err = directory.RetireAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(factory.pools) != 1 || !factory.pools[0].closed {
		t.Fatal("old provider handle survived retirement")
	}
	next, err := directory.Pool(binding)
	if err != nil {
		t.Fatal(err)
	}
	if next == old {
		t.Fatal("retired pool reused")
	}
	if err = next.Refresh(t.Context(), RefreshRequest{Actor: "operator", Operation: RefreshRequested}); err != nil {
		t.Fatal(err)
	}
	if len(factory.pools) != 2 || factory.pools[1].closed {
		t.Fatal("replacement pool not usable")
	}
}
