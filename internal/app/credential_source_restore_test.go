package app

import (
	"context"
	"errors"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

type credentialRestoreLease struct {
	snapshot   accesssnapshot.AuthorizationSnapshot
	snapshotID int64
	identity   projectgraph.ServingIdentity
	releases   int
	runtime    projectruntime.Runtime
}

func (l *credentialRestoreLease) Runtime() projectruntime.Runtime {
	if l.runtime != nil {
		return l.runtime
	}
	return l
}
func (l *credentialRestoreLease) Close() error { return nil }
func (l *credentialRestoreLease) Identity() projectgraph.ServingIdentity {
	if l.identity.GenerationID != "" {
		return l.identity
	}
	return l.snapshot.Identity()
}
func (l *credentialRestoreLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}
func (l *credentialRestoreLease) DuckLakeSnapshotID() int64 { return l.snapshotID }
func (l *credentialRestoreLease) Release()                  { l.releases++ }

type credentialRestoreHost struct {
	lease               *credentialRestoreLease
	leaseErr, healthErr error
	fence               bool
	current             servingstate.ID
	acquired            int
	reconciles          int
	reconcile           func() error
}

func (h *credentialRestoreHost) Acquire(context.Context) (projectruntime.Lease, error) {
	if h.leaseErr != nil {
		return nil, h.leaseErr
	}
	h.acquired++
	return h.lease, nil
}
func (h *credentialRestoreHost) CurrentServingStateID() servingstate.ID { return h.current }
func (h *credentialRestoreHost) AcquireCutoverFence(context.Context) (func(), error) {
	h.fence = true
	return func() { h.fence = false }, nil
}
func (h *credentialRestoreHost) LeaseRenewalError() error { return h.healthErr }
func (h *credentialRestoreHost) ReconcileSealed(context.Context, servingstate.ID) error {
	h.reconciles++
	return h.reconcile()
}

type credentialRestoreReader struct {
	deploymentmodule.NativeDeliveryReader
	target      deploymentpostgres.DeliveryOperatorSnapshot
	generation  deploymentpostgres.DeliveryGeneration
	seal        deploymentpostgres.SnapshotSeal
	publication deploymentpostgres.DeliveryPublication
}

func (r *credentialRestoreReader) LoadGeneration(context.Context, string) (deploymentpostgres.DeliveryGeneration, error) {
	return r.generation, nil
}
func (r *credentialRestoreReader) LoadSnapshotSeal(context.Context, string) (deploymentpostgres.SnapshotSeal, error) {
	return r.seal, nil
}
func (r *credentialRestoreReader) LoadPublication(context.Context, string) (deploymentpostgres.DeliveryPublication, error) {
	return r.publication, nil
}
func (r *credentialRestoreReader) OperatorSnapshot(context.Context, string) (deploymentpostgres.DeliveryOperatorSnapshot, error) {
	return r.target, nil
}

type credentialRestoreEvidence struct {
	calls int
	err   error
}

func (e *credentialRestoreEvidence) BindingEvidence(context.Context, string, string) ([]analyticsmodule.ActiveRuntimeBindingEvidence, error) {
	e.calls++
	return nil, e.err
}

func credentialRestoreFixture(t *testing.T) (*credentialRestoreHost, *credentialRestoreReader, *credentialRestoreEvidence) {
	t.Helper()
	snapshot := tusSnapshot(t, "principal", "connection_sales", false)
	identity := snapshot.Identity()
	host := &credentialRestoreHost{current: servingstate.ID(identity.GenerationID), lease: &credentialRestoreLease{snapshot: snapshot, snapshotID: 10}, reconcile: func() error { return nil }}
	reader := &credentialRestoreReader{
		target:      deploymentpostgres.DeliveryOperatorSnapshot{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, TargetID: "target:test", TargetRevision: 2, ActiveGenerationID: identity.GenerationID, ActivePublicationID: "publication:test"},
		generation:  deploymentpostgres.DeliveryGeneration{GenerationID: identity.GenerationID, TargetID: "target:test", CandidateID: "candidate:test", SnapshotSealID: "seal:test", ServingArtifactDigest: "digest:test"},
		seal:        deploymentpostgres.SnapshotSeal{SealID: "seal:test", CandidateID: "candidate:test", DuckLakeSnapshotID: 10, ServingArtifactDigest: "digest:test", QualifiedAt: time.Now()},
		publication: deploymentpostgres.DeliveryPublication{PublicationID: "publication:test", TargetID: "target:test", GenerationID: identity.GenerationID, CandidateID: "candidate:test", SnapshotSealID: "seal:test", State: "committed", ResultTargetRevision: 2},
	}
	return host, reader, &credentialRestoreEvidence{}
}

func TestSourceCredentialRestoreReusesOnlyProvedCurrentRuntime(t *testing.T) {
	host, reader, evidence := credentialRestoreFixture(t)
	cache, err := resultcache.New(resultcache.Limits{RuntimeEntries: 1, RuntimeBytes: 1024, NodeEntries: 1, NodeBytes: 1024})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	id := resultcache.ScopeID{RuntimeID: reader.target.ActiveGenerationID}
	scope, err := cache.OpenScope(id)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	host.reconcile = func() error { _, err := cache.OpenScope(id); return err }
	require.NoError(t, restoreSourceCredentialGeneration(t.Context(), host, reader, evidence, reader.target), "startup already owns this exact generation's cache scope")
	require.Zero(t, host.reconciles)
	require.Equal(t, 1, evidence.calls)
	require.Equal(t, 1, host.lease.releases)
	require.False(t, host.fence)
}

type credentialRestorePinnedRuntime struct{ snapshotID int64 }

func (r credentialRestorePinnedRuntime) Close() error              { return nil }
func (r credentialRestorePinnedRuntime) DuckLakeSnapshotID() int64 { return r.snapshotID }

type credentialRestoreUnpinnedRuntime struct{}

func (credentialRestoreUnpinnedRuntime) Close() error { return nil }

func credentialInstalledRecord(target deploymentpostgres.DeliveryOperatorSnapshot) credentialmodule.ActivationRecord {
	return credentialmodule.ActivationRecord{Resource: credentialmodule.ValidationResource{ScopeKind: "connection", TargetID: target.TargetID, ProjectID: target.ProjectID, Environment: target.Environment, ResourceID: "connection:sales"}, Status: credentialmodule.ActivationStatus{State: "committed", GenerationID: target.ActiveGenerationID, PublicationID: target.ActivePublicationID}}
}

func TestSourceCredentialRestoreUsesFactoryOwnedSealedSnapshot(t *testing.T) {
	host, reader, evidence := credentialRestoreFixture(t)
	host.lease.snapshotID = 0
	host.lease.runtime = credentialRestorePinnedRuntime{snapshotID: reader.seal.DuckLakeSnapshotID}
	require.NoError(t, restoreSourceCredentialGeneration(t.Context(), host, reader, evidence, reader.target), "sealed runtime factory owns the positive pinned snapshot and lease")
	require.Zero(t, host.reconciles)
}

func TestSourceCredentialInstallReusesCurrentRuntimeAfterCompletionFailure(t *testing.T) {
	host, reader, evidence := credentialRestoreFixture(t)
	cache, err := resultcache.New(resultcache.Limits{RuntimeEntries: 1, RuntimeBytes: 1024, NodeEntries: 1, NodeBytes: 1024})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	id := resultcache.ScopeID{RuntimeID: reader.target.ActiveGenerationID}
	scope, err := cache.OpenScope(id)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	host.reconcile = func() error { _, err := cache.OpenScope(id); return err }
	record := credentialInstalledRecord(reader.target)
	require.NoError(t, installSourceCredentialGeneration(t.Context(), host, reader, evidence, reader.target.TargetID, record))
	require.Zero(t, host.reconciles, "retry after successful install must preserve the live runtime's owned scope")
	require.Equal(t, 1, evidence.calls)
}

func TestSourceCredentialInstallRejectsRecordOutsideCurrentPublication(t *testing.T) {
	for _, scenario := range []string{"target", "project", "environment", "generation", "publication", "state"} {
		t.Run(scenario, func(t *testing.T) {
			host, reader, evidence := credentialRestoreFixture(t)
			record := credentialInstalledRecord(reader.target)
			switch scenario {
			case "target":
				record.Resource.TargetID = "other"
			case "project":
				record.Resource.ProjectID = "other"
			case "environment":
				record.Resource.Environment = "test"
			case "generation":
				record.Status.GenerationID = "other"
			case "publication":
				record.Status.PublicationID = "other"
			case "state":
				record.Status.State = "prepared"
			}
			err := installSourceCredentialGeneration(t.Context(), host, reader, evidence, reader.target.TargetID, record)
			require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
			require.Zero(t, host.reconciles)
			require.Zero(t, evidence.calls)
		})
	}
}

func TestSourceCredentialRestoreReconcilesMissingOrChangedRuntime(t *testing.T) {
	for _, scenario := range []string{"missing", "changed", "different-lease"} {
		host, reader, evidence := credentialRestoreFixture(t)
		switch scenario {
		case "missing":
			host.current = ""
			host.leaseErr = errors.New("no current runtime")
		case "changed":
			reader.target.ActiveGenerationID = "generation:replacement"
		case "different-lease":
			host.lease.identity = host.lease.Identity()
			host.lease.identity.GenerationID = "generation:replacement"
		}
		host.reconcile = func() error {
			require.False(t, host.fence, "fallback must not retain the non-reentrant cutover fence")
			require.Equal(t, host.acquired, host.lease.releases, "fallback must release any prior runtime lease")
			return nil
		}
		require.NoError(t, restoreSourceCredentialGeneration(t.Context(), host, reader, evidence, reader.target))
		require.Equal(t, 1, host.reconciles)
		require.Zero(t, evidence.calls)
		require.False(t, host.fence)
	}
}

func TestSourceCredentialRestoreRejectsUnhealthyOrUnprovedCurrentRuntime(t *testing.T) {
	failure := errors.New("unusable runtime")
	for _, scenario := range []string{"acquire", "health", "snapshot", "runtime-unpinned", "runtime-mismatched", "seal", "generation", "publication", "evidence", "pointer-moved"} {
		t.Run(scenario, func(t *testing.T) {
			host, reader, evidence := credentialRestoreFixture(t)
			target := reader.target
			switch scenario {
			case "acquire":
				host.leaseErr = failure
			case "health":
				host.healthErr = failure
			case "snapshot":
				host.lease.snapshotID = 0
			case "runtime-unpinned":
				host.lease.runtime = credentialRestoreUnpinnedRuntime{}
			case "runtime-mismatched":
				host.lease.runtime = credentialRestorePinnedRuntime{snapshotID: 11}
			case "seal":
				reader.seal.CandidateID = "candidate:foreign"
			case "generation":
				reader.generation.TargetID = "target:foreign"
			case "publication":
				reader.publication.State = "pending"
			case "evidence":
				evidence.err = failure
			case "pointer-moved":
				reader.target.TargetRevision++
			}
			err := restoreSourceCredentialGeneration(t.Context(), host, reader, evidence, target)
			if scenario == "acquire" || scenario == "health" || scenario == "evidence" {
				require.ErrorIs(t, err, failure)
			} else {
				require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
			}
			require.Zero(t, host.reconciles, "a matching but unproved runtime must keep provider work closed")
			require.Equal(t, host.acquired, host.lease.releases)
			require.False(t, host.fence)
		})
	}
}
