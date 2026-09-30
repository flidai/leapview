package ui

import (
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestPipelineRunDetailSignalNormalizesTabAndKeepsEmptyEvents(t *testing.T) {
	attempts := []uisignals.PipelineRunAttemptSignal{{Number: 1, Status: "failed", ClaimedAt: "2026-09-21T13:00:00Z"}}
	page := PipelineRunDetailPageSignal(PipelineRunDetailPageState{
		Title: "Run 123", ActiveTab: "unknown", Status: "prepared", StatusLabel: "Finalizing",
		EventsTruncated: true,
		Execution: uisignals.PipelineRunExecutionSignal{
			ValidationOutcome: "unknown", ValidationTimingAvailable: false,
			PublicationOutcome: "unverified", Attempts: attempts, Models: []uisignals.PipelineRunModelSignal{},
		},
		Events: []uisignals.PipelineRunEventSignal{},
	})
	if page.Kind != "pipeline_run_detail" || page.ActiveTab != "execution" || page.Status != "prepared" || page.StatusLabel != "Finalizing" {
		t.Fatalf("page state = %#v", page)
	}
	if page.Events == nil || len(page.Events) != 0 || !page.EventsTruncated {
		t.Fatalf("events = %#v, truncated = %v; want an explicit empty slice and truncation flag", page.Events, page.EventsTruncated)
	}
	if page.Execution.ValidationOutcome != "unknown" || page.Execution.ValidationTimingAvailable || len(page.Execution.Attempts) != 1 || page.Execution.Attempts[0].Status != "failed" {
		t.Fatalf("lifecycle diagnostic evidence = %#v; validation and persisted attempt must pass through unchanged", page.Execution)
	}
}

func TestPipelineRunHistoricalGraphUsesOnlyTheRunServingGeneration(t *testing.T) {
	const projectID = "project:test"
	const generation = "generation:run"
	graph := servingstate.AssetGraph{
		Assets: []servingstate.Asset{
			{ID: "pipeline:sales", ProjectID: projectID, ServingStateID: servingstate.ID(generation), Type: "refresh_pipeline", Key: "sales", Title: "Historical Sales"},
			{ID: "semantic:sales", ProjectID: projectID, ServingStateID: servingstate.ID(generation), Type: "semantic_model", Key: "sales", Title: "Sales semantic model"},
			{ID: "model:historical", ProjectID: projectID, ServingStateID: servingstate.ID(generation), Type: "model", Key: "historical", Title: "Historical model"},
			{ID: "pipeline:sales", ProjectID: projectID, ServingStateID: "generation:current", Type: "refresh_pipeline", Key: "sales", Title: "Current Sales"},
			{ID: "model:current-only", ProjectID: projectID, ServingStateID: "generation:current", Type: "model", Key: "current-only", Title: "Current model"},
		},
		Edges: []servingstate.AssetEdge{
			{ID: "historical-pipeline-semantic", ProjectID: projectID, ServingStateID: servingstate.ID(generation), FromAssetID: "pipeline:sales", ToAssetID: "semantic:sales", Type: "refreshes"},
			{ID: "historical-semantic-model", ProjectID: projectID, ServingStateID: servingstate.ID(generation), FromAssetID: "semantic:sales", ToAssetID: "model:historical", Type: "uses_model"},
			{ID: "current-pipeline-model", ProjectID: projectID, ServingStateID: "generation:current", FromAssetID: "pipeline:sales", ToAssetID: "model:current-only", Type: "refreshes"},
		},
	}

	lineage, found, err := PipelineRunHistoricalGraph(projectID, "pipeline:sales", generation, graph)
	if err != nil || !found {
		t.Fatalf("historical graph = found %v, err %v", found, err)
	}
	seen := make(map[string]bool, len(lineage.Nodes))
	for _, node := range lineage.Nodes {
		seen[node.ID] = true
		if node.Label == "Current Sales" || node.ID == "model:current-only" {
			t.Fatalf("run graph leaked current generation node %#v", node)
		}
	}
	if seen["pipeline:sales"] {
		t.Fatalf("historical data-flow graph retained the orchestration pipeline node: %#v", lineage.Nodes)
	}
	if !seen["semantic:sales"] || !seen["model:historical"] {
		t.Fatalf("historical graph omitted the selected semantic model or its dependency: %#v", lineage.Nodes)
	}
	if len(lineage.Edges) != 1 || lineage.Edges[0].Source != "model:historical" || lineage.Edges[0].Target != "semantic:sales" {
		t.Fatalf("historical edges = %#v, want the model feeding the selected semantic model", lineage.Edges)
	}
	if _, err := projectgraph.NewResourceID(projectID); err != nil {
		t.Fatalf("test project ID is invalid: %v", err)
	}
}
