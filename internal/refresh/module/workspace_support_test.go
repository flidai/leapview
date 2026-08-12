package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/workspace"
)

func TestWorkspaceRefreshExecutesGeneratedUICommandContract(t *testing.T) {
	var queued, completed bool
	support := WorkspaceSupport{
		QueuePipeline: func(ctx context.Context, input refreshrun.QueuePipelineInput) (refreshrun.QueueAssetResult, error) {
			operationID, ok := apigencommand.OperationID(ctx)
			if !ok || operationID != string(refreshgen.GenOperationCreateRefreshRun) {
				t.Fatalf("queued operation = %q, present=%v", operationID, ok)
			}
			if input.WorkspaceID != "sales" || input.PipelineID != "daily" {
				t.Fatalf("queue input = %#v", input)
			}
			queued = true
			return refreshrun.QueueAssetResult{Run: refreshrun.RunRecord{ID: "run_1"}}, nil
		},
		RunCreated: func(ctx context.Context, run refreshrun.RunRecord) error {
			executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, nil)
			if err != nil {
				return err
			}
			err = executor.Execute(ctx, string(refreshgen.GenOperationCreateRefreshRun), apigencommand.Execution{
				BestEffortAudit: func(context.Context, apigencommand.Contract) error { return nil },
			})
			completed = err == nil && run.ID == "run_1"
			return err
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/workspaces/sales/assets/pipeline/refresh", nil)
	request.Header.Set(uicommand.HeaderOperationID, string(refreshgen.GenOperationCreateRefreshRun))
	request.Header.Set("X-Request-ID", "request-1")
	asset := workspace.AssetView{ID: "pipeline", Key: "sales.daily", Type: string(workspace.AssetTypeRefreshPipeline)}
	if err := support.RefreshAsset(t.Context(), request, "sales", asset, nil, nil); err != nil {
		t.Fatalf("refresh asset: %v", err)
	}
	if !queued || !completed {
		t.Fatalf("queued=%v completed=%v", queued, completed)
	}
}
