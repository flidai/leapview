package stream

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
)

func TestWindowCarryPreservesLatestTargetAndFilterBoundary(t *testing.T) {
	for _, changedFilter := range []bool{false, true} {
		name := "same_filter"
		if changedFilter {
			name = "changed_filter"
		}
		t.Run(name, func(t *testing.T) {
			filters := dashboard.Filters{InteractionRevision: 1}.WithDefaults()
			old := command.Target{Kind: command.TargetWindow, ID: "orders", WindowRequest: dashboard.TableRequest{Start: 100, RequestSeq: 1}}
			other := command.Target{Kind: command.TargetWindow, ID: "other", WindowRequest: dashboard.TableRequest{Start: 200, RequestSeq: 1}}
			latest := command.Target{Kind: command.TargetWindow, ID: "orders", WindowRequest: dashboard.TableRequest{Start: 5000, RequestSeq: 2}}
			c := &Coordinator{active: &activeRefresh{refresh: Refresh{Filters: filters}, targetPlans: map[string]command.Target{old.Key(): old, other.Key(): other}}}
			nextFilters := filters
			if changedFilter {
				nextFilters.InteractionRevision++
			}
			p := RefreshPreparation{Command: "visual_window", Filters: nextFilters, Targets: []string{latest.Key()}, Plan: command.RefreshPlan{Command: "visual_window", Targets: []command.Target{latest}}}
			c.carryUnfinishedWindowTargets(&p, nextFilters)
			plan := p.Plan.(command.RefreshPlan)
			want := 2
			if changedFilter {
				want = 1
			}
			if len(plan.Targets) != want || len(p.Targets) != want {
				t.Fatalf("targets = %#v, keys = %v", plan.Targets, p.Targets)
			}
			if plan.Targets[0].WindowRequest.Start != 5000 || plan.Targets[0].WindowRequest.RequestSeq != 2 {
				t.Fatalf("newest request replaced: %#v", plan.Targets[0])
			}
		})
	}
}
