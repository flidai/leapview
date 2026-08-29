package materialize

import (
	"strings"
	"testing"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func identityDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func identityDependency(t *testing.T) resultidentity.Dependency {
	t.Helper()
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{
		SemanticModelID: "semantic_sales", SemanticModelDigest: identityDigest('a'),
		Relations: []resultidentity.RelationRevision{{RelationID: "orders", RevisionDigest: identityDigest('b')}}, BindingFingerprint: identityDigest('c'),
		Execution: resultidentity.ExecutionIdentity{PlannerDigest: identityDigest('d'), RuntimeDigest: identityDigest('e'), CapabilityDigest: identityDigest('f'), SettingsDigest: identityDigest('0')}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dependency
}

func TestDependencyCacheKeyUsesStableTypedPartition(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: 4, PartitionBytes: 1 << 20, NodeEntries: 4, NodeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	production, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionCandidate, ProjectID: "project_sales", Environment: "prod", CandidateID: "candidate-1"})
	if err != nil {
		t.Fatal(err)
	}
	prodScope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "prod", PartitionID: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	candScope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "candidate", PartitionID: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	prod := newQueryResultCacheWithScopeAndPartition(prodScope, production)
	cand := newQueryResultCacheWithScopeAndPartition(candScope, candidate)
	query := dataquery.Query{Kind: dataquery.KindSemanticRows, Target: "orders", EffectivePolicyFingerprint: identityDigest('9')}
	dependency := identityDependency(t)
	prodKey, _, err := prod.cacheKeyWithDependency(query, dependency)
	if err != nil {
		t.Fatal(err)
	}
	candKey, _, err := cand.cacheKeyWithDependency(query, dependency)
	if err != nil {
		t.Fatal(err)
	}
	if prodKey == candKey {
		t.Fatal("production and candidate partitions shared a cache key")
	}
	if _, _, err := prod.cacheKeyWithDependency(query, dependency); err != nil {
		t.Fatal(err)
	}
	query.EffectivePolicyFingerprint = "not-a-digest"
	if _, _, err := prod.cacheKeyWithDependency(query, dependency); err == nil {
		t.Fatal("invalid policy fingerprint was accepted on production partition")
	}
}

func TestProductionDependencyCacheKeyFailsClosedWithoutPolicy(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: 1, PartitionBytes: 1 << 20, NodeEntries: 1, NodeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "missing-policy", PartitionID: "missing-policy"})
	if err != nil {
		t.Fatal(err)
	}
	cache := newQueryResultCacheWithScopeAndPartition(scope, partition)
	if _, _, err := cache.cacheKeyWithDependency(dataquery.Query{Kind: dataquery.KindSemanticRows, Target: "orders"}, identityDependency(t)); err == nil {
		t.Fatal("missing production policy fingerprint did not fail closed")
	}
	_ = analyticscache.CacheKeyVersion // contract is intentionally referenced by this production-path test.
}
