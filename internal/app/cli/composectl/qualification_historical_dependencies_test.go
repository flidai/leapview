package composectl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/deployment"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

// qualificationHistoricalTransitionGrants derives the publisher's exact read
// scope from the immutable schema-32 CFO source graph. This keeps transition
// authorization aligned with the dependencies the native delivery planner
// actually evaluates while avoiding future-resource or project-wide grants.
func qualificationHistoricalTransitionGrants(t *testing.T, seed qualificationHistoricalSeed) []admincli.AccessTransitionGrantIntent {
	t.Helper()
	require.NotEmpty(t, seed.SourceRoot, "the transition grant graph must come from the pinned predecessor source tree")
	graph, err := projectcompiler.CompileGraph(seed.SourceRoot)
	require.NoError(t, err, "compile the exact predecessor CFO graph for scoped publisher dependencies")
	plan, err := deployment.DeliveryAuthorizationPlanFromBundle(
		projectgraph.ResourceID(seed.ProjectID), seed.TargetID, graph, projectcompiler.BundlePlan{}, nil,
	)
	require.NoError(t, err, "derive the native planner's resource dependencies from the predecessor graph")

	expectedModels := map[string]struct{}{
		"model:cash_forecast": {}, "model:cash_scenarios": {}, "model:cash_weeks": {},
		"model:finance_countries": {}, "model:finance_dates": {}, "model:finance_discount_bands": {},
		"model:finance_products": {}, "model:finance_segments": {}, "model:financial_performance": {},
		"model:pnl_lines": {}, "model:pnl_statement": {}, "model:variance_driver_dimension": {},
		"model:variance_drivers": {},
	}
	expectedPairs := map[string]struct{}{
		historicalDependencyKey(access.ActionSourceRead, projectgraph.KindSource, "source:finance.financials"):          {},
		historicalDependencyKey(access.ActionSemanticConsume, projectgraph.KindSemanticModel, "semantic-model:finance"): {},
		historicalDependencyKey(access.ActionConnectionUse, projectgraph.KindConnection, "connection:finance_files"):    {},
	}
	for modelID := range expectedModels {
		expectedPairs[historicalDependencyKey(access.ActionModelRead, projectgraph.KindModel, modelID)] = struct{}{}
	}
	observedPairs := make(map[string]struct{}, len(plan.Dependencies))
	for _, dependency := range plan.Dependencies {
		key := historicalDependencyKey(dependency.Action, dependency.Resource.Kind(), dependency.Resource.ID().String())
		observedPairs[key] = struct{}{}
	}
	require.Equal(t, expectedPairs, observedPairs,
		"only the source, model, semantic-model, and connection pairs in the pinned CFO graph may be granted")
	observedModels := make(map[string]struct{})
	for _, resource := range graph.Resources() {
		if resource.Kind == projectgraph.KindModel {
			observedModels[resource.ID.String()] = struct{}{}
		}
	}
	require.Equal(t, expectedModels, observedModels, "the predecessor CFO model set must remain explicitly pinned")

	grantsByResource := make(map[string]*admincli.AccessTransitionGrantIntent)
	add := func(principalID string, resourceID projectgraph.ResourceID, kind projectgraph.Kind, action access.Action) {
		key := principalID + "\x00" + string(kind) + "\x00" + resourceID.String()
		grant := grantsByResource[key]
		if grant == nil {
			grant = &admincli.AccessTransitionGrantIntent{
				GrantID:   historicalTransitionGrantID(principalID, kind, resourceID),
				Name:      "Historical exact access to " + resourceID.String(),
				Principal: principalID, ResourceID: resourceID.String(), ResourceKind: string(kind),
			}
			grantsByResource[key] = grant
		}
		for _, existing := range grant.Actions {
			if existing == string(action) {
				return
			}
		}
		grant.Actions = append(grant.Actions, string(action))
	}
	for _, dependency := range plan.Dependencies {
		add(seed.PublisherPrincipalID, dependency.Resource.ID(), dependency.Resource.Kind(), dependency.Action)
	}
	add(seed.PublisherPrincipalID, projectgraph.ResourceID("connection:finance_files"), projectgraph.KindConnection, access.ActionConnectionManage)
	add(seed.ViewerPrincipalID, projectgraph.ResourceID(seed.DashboardID), projectgraph.KindDashboard, access.ActionDashboardRead)
	add(seed.ViewerPrincipalID, projectgraph.ResourceID("semantic-model:finance"), projectgraph.KindSemanticModel, access.ActionSemanticConsume)

	grants := make([]admincli.AccessTransitionGrantIntent, 0, len(grantsByResource))
	for _, grant := range grantsByResource {
		sort.Strings(grant.Actions)
		grants = append(grants, *grant)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].GrantID < grants[j].GrantID })
	require.Len(t, grants, 18, "publisher dependency scope and the viewer's two exact grants must remain separate")
	semanticPrincipals := map[string]struct{}{}
	viewerGrants := map[string][]string{}
	for _, grant := range grants {
		if grant.ResourceID == "semantic-model:finance" {
			semanticPrincipals[grant.Principal] = struct{}{}
		}
		if grant.Principal == seed.ViewerPrincipalID {
			viewerGrants[grant.ResourceID] = grant.Actions
		}
	}
	require.Equal(t, map[string]struct{}{seed.PublisherPrincipalID: {}, seed.ViewerPrincipalID: {}}, semanticPrincipals,
		"publisher dependency and viewer query grants must coexist as separate principal-resource assignments")
	require.Equal(t, map[string][]string{
		seed.DashboardID:         {string(access.ActionDashboardRead)},
		"semantic-model:finance": {string(access.ActionSemanticConsume)},
	}, viewerGrants, "the viewer must retain exactly dashboard.read and semantic.consume")
	return grants
}

func historicalDependencyKey(action access.Action, kind projectgraph.Kind, resourceID string) string {
	return string(action) + "\x00" + string(kind) + "\x00" + resourceID
}

func historicalTransitionGrantID(principalID string, kind projectgraph.Kind, resourceID projectgraph.ResourceID) string {
	sum := sha256.Sum256([]byte(principalID + "\x00" + string(kind) + "\x00" + resourceID.String()))
	return fmt.Sprintf("typed-historical-%s", hex.EncodeToString(sum[:12]))
}
