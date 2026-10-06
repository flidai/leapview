package application

import (
	"github.com/flidai/leapview/internal/analytics/dataquery"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	"testing"
)

func TestURLExportExecutesBeyondInteractiveChartLimit(t *testing.T) {
	executor := &testExecutor{result: dataquery.Result{Rows: make([]dataquery.Row, 350)}}
	service := mustService(t, &testRepository{}, &testProvider{}, &testAuthorizer{}, executor)
	spec := testSpec()
	spec.Limit = 10
	result, err := service.ExecuteSpec(t.Context(), saved.ExecuteSpecRequest{ProjectID: "project:sales", ActorID: "owner", Spec: spec, Operation: "saved_exploration_url_export"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Query.Limit != saved.ExportDefaultMaxRows+1 || result.Query.Offset != 0 || len(result.Result.Rows) != 350 {
		t.Fatalf("export remains capped by chart limit: query=%#v rows=%d", result.Query, len(result.Result.Rows))
	}
	if spec.Limit != 10 {
		t.Fatal("export rewrote authored chart limit")
	}
}

func TestExportBudgetChangesOnlyTransportWindow(t *testing.T) {
	for _, savedResource := range []bool{false, true} {
		repo := seededRepository(t)
		executor := &testExecutor{}
		service := mustService(t, repo, &testProvider{}, &testAuthorizer{}, executor)
		var result saved.ExecuteResult
		var err error
		if savedResource {
			result, err = service.Execute(t.Context(), saved.ExecuteRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Operation: "saved_exploration_export", ExportMaxRows: 500})
		} else {
			result, err = service.ExecuteSpec(t.Context(), saved.ExecuteSpecRequest{ProjectID: "project:sales", ActorID: "owner", Spec: testSpec(), Operation: "saved_exploration_url_export", ExportMaxRows: 500})
		}
		if err != nil {
			t.Fatal(err)
		}
		if result.Query.Limit != 501 || result.Query.Offset != 0 || result.Query.PrincipalID != "owner" || len(result.Query.Fields) == 0 || len(result.Query.Metrics) == 0 {
			t.Fatalf("export changed governed selection or budget: %#v", result.Query)
		}
	}
}
