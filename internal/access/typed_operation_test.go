package access

import (
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestTypedOperationRequirementResolvesExactTargetAndDependencies(t *testing.T) {
	service := NewTypedOperationRequirementService()
	requirement, err := service.Requirement(ActionSemanticQuery, string(TypedOperationResolverSemanticModel))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := NewResourceRef("semantic_sales", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := requirement.ResolvePairs("project_sales", resource)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Action != ActionSemanticQuery || pairs[1].Action != ActionSemanticConsume {
		t.Fatalf("resolved pairs = %#v, want query and consume dependencies", pairs)
	}
	for _, pair := range pairs {
		if pair.Target.ProjectID != "project_sales" || pair.Target.ResourceID != "semantic_sales" || pair.Target.ResourceKind != projectgraph.KindSemanticModel {
			t.Fatalf("resolved pair target = %#v, want exact semantic target", pair.Target)
		}
	}
}

func TestTypedOperationRequirementFailsClosedForUnknownOrMissingMetadata(t *testing.T) {
	service := NewTypedOperationRequirementService()
	for name, test := range map[string]struct {
		action   Action
		resolver string
		want     error
	}{
		"unknown action":      {Action("dashboard.unknown"), "dashboard", ErrUnknownPermissionAction},
		"missing action":      {"", "dashboard", ErrTypedOperationActionRequired},
		"missing resolver":    {ActionDashboardRead, "", ErrTypedOperationResolverRequired},
		"unknown resolver":    {ActionDashboardRead, "dashboards", ErrUnknownTypedOperationResolver},
		"mismatched resolver": {ActionDashboardRead, "pipeline", ErrInvalidPermissionCatalog},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Requirement(test.action, test.resolver)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func TestTypedOperationRequirementRejectsWrongResolvedTarget(t *testing.T) {
	service := NewTypedOperationRequirementService()
	requirement, err := service.Requirement(ActionDashboardRead, string(TypedOperationResolverDashboard))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := NewResourceRef("pipeline_daily", projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requirement.ResolvePairs("project_sales", resource); err == nil {
		t.Fatal("dashboard resolver accepted a pipeline target")
	}
}

func TestTypedOperationRequirementResolvesProjectScopedPairs(t *testing.T) {
	service := NewTypedOperationRequirementService()
	for _, test := range []struct {
		action   Action
		resolver TypedOperationResolver
	}{
		{ActionProjectSettingsRead, TypedOperationResolverProject},
		{ActionConnectionCreate, TypedOperationResolverProject},
		{ActionDeliveryPlan, TypedOperationResolverDelivery},
	} {
		t.Run(string(test.action), func(t *testing.T) {
			requirement, err := service.Requirement(test.action, string(test.resolver))
			if err != nil {
				t.Fatal(err)
			}
			pairs, err := requirement.ResolvePairs("project_demo")
			if err != nil {
				t.Fatal(err)
			}
			if len(pairs) != 1 || pairs[0].Action != test.action || pairs[0].Target.Scope != PermissionScopeProject || pairs[0].Target.ProjectID != "project_demo" || pairs[0].Target.ResourceID != "" {
				t.Fatalf("resolved pairs = %#v, want one project-scoped pair", pairs)
			}
		})
	}
}

func TestTypedOperationRequirementResolvesSourceModelAndPipelineTargets(t *testing.T) {
	service := NewTypedOperationRequirementService()
	for _, test := range []struct {
		action   Action
		resolver TypedOperationResolver
		kind     projectgraph.Kind
	}{
		{ActionSourceRead, TypedOperationResolverSource, projectgraph.KindSource},
		{ActionModelRead, TypedOperationResolverModel, projectgraph.KindModel},
		{ActionPipelineRun, TypedOperationResolverPipeline, projectgraph.KindPipeline},
	} {
		t.Run(string(test.action), func(t *testing.T) {
			requirement, err := service.Requirement(test.action, string(test.resolver))
			if err != nil {
				t.Fatal(err)
			}
			resource, err := NewResourceRef("resource_one", test.kind)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := requirement.PermissionPair("project_demo", resource)
			if err != nil {
				t.Fatal(err)
			}
			if pair.Action != test.action || pair.Target.ResourceKind != test.kind || pair.Target.ResourceID != "resource_one" || pair.Target.Scope != PermissionScopeResource {
				t.Fatalf("resolved pair = %#v", pair)
			}
		})
	}
}

func TestTypedOperationRequirementResolvesInstancePairsOnlyWithInstanceResolver(t *testing.T) {
	service := NewTypedOperationRequirementService()
	requirement, err := service.Requirement(ActionPlatformSettingsRead, string(TypedOperationResolverInstance))
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := requirement.ResolveInstancePairs("instance_primary")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Target.Scope != PermissionScopeInstance || pairs[0].Target.InstanceID != "instance_primary" {
		t.Fatalf("resolved instance pairs = %#v", pairs)
	}
	if _, err := requirement.ResolvePairs("project_demo"); err == nil {
		t.Fatal("instance resolver accepted project pair resolution")
	}
}
