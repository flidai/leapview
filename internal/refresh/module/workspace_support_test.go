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

func TestWorkspaceRefreshRetryAndCancelExecuteGeneratedUICommandContracts(t *testing.T) {
	asset := workspace.AssetView{ID: "refresh_pipeline:daily", Key: "sales.daily", Type: string(workspace.AssetTypeRefreshPipeline)}
	prior := refreshrun.RunRecord{
		ID: "run_failed", WorkspaceID: "sales", Environment: "dev", TargetType: refreshrun.TargetRefreshPipeline,
		TargetID: "sales.daily", Status: refreshrun.RunStatusFailed,
	}
	reader := workspaceSupportRunReader{run: prior}
	var retryQueued, cancelled bool
	support := WorkspaceSupport{
		Runs: func() (RunReader, error) { return reader, nil },
		QueuePipeline: func(ctx context.Context, input refreshrun.QueuePipelineInput) (refreshrun.QueueAssetResult, error) {
			operationID, ok := apigencommand.OperationID(ctx)
			if !ok || operationID != string(refreshgen.GenOperationCreateRefreshRun) || input.TriggerType != refreshrun.TriggerRetry || input.RetryOf != prior.ID {
				t.Fatalf("retry invocation = operation %q, input %#v", operationID, input)
			}
			retryQueued = true
			return refreshrun.QueueAssetResult{Run: refreshrun.RunRecord{ID: "run_retry"}}, nil
		},
		RunCreated: func(ctx context.Context, _ refreshrun.RunRecord) error {
			return executeWorkspaceSupportCommand(ctx, string(refreshgen.GenOperationCreateRefreshRun))
		},
		CancelRun: func(_ context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
			if workspaceID != "sales" || runID != prior.ID {
				t.Fatalf("cancel target = %s/%s", workspaceID, runID)
			}
			cancelled = true
			row := prior
			row.Status = refreshrun.RunStatusCancelled
			return row, nil
		},
		RunCancelled: func(ctx context.Context, _ refreshrun.RunRecord) error {
			return executeWorkspaceSupportCommand(ctx, string(refreshgen.GenOperationCancelRefreshRun))
		},
	}

	retryRequest := httptest.NewRequest(http.MethodPost, "/pipelines/command", nil)
	retryRequest.Header.Set(uicommand.HeaderOperationID, string(refreshgen.GenOperationCreateRefreshRun))
	retryRequest.Header.Set("X-Request-ID", "retry-request")
	if err := support.RetryAsset(t.Context(), retryRequest, "sales", asset, nil, nil, prior.ID); err != nil {
		t.Fatalf("retry asset: %v", err)
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "/pipelines/command", nil)
	cancelRequest.Header.Set(uicommand.HeaderOperationID, string(refreshgen.GenOperationCancelRefreshRun))
	cancelRequest.Header.Set("X-Request-ID", "cancel-request")
	if err := support.CancelRefreshRun(t.Context(), cancelRequest, "sales", "daily", prior.ID); err != nil {
		t.Fatalf("cancel refresh run: %v", err)
	}
	if !retryQueued || !cancelled {
		t.Fatalf("retryQueued=%v cancelled=%v", retryQueued, cancelled)
	}
}

type workspaceSupportRunReader struct{ run refreshrun.RunRecord }

func (r workspaceSupportRunReader) GetRun(context.Context, string, string) (refreshrun.RunRecord, error) {
	return r.run, nil
}

func (workspaceSupportRunReader) ListTargetRuns(context.Context, string, string, string, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return nil, nil
}

func (workspaceSupportRunReader) LatestSuccessfulTargetRun(context.Context, string, string, string, string) (refreshrun.RunRecord, bool, error) {
	return refreshrun.RunRecord{}, false, nil
}

func executeWorkspaceSupportCommand(ctx context.Context, operationID string) error {
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(context.Context, apigencommand.Contract) error { return nil },
	})
}
