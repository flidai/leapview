package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	agentcap "github.com/flidai/leapview/internal/agent"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
)

func TestDataAuthorizationPassesSelectedColumnMaskToExecution(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_masked", Email: "masked@example.com", DisplayName: "Masked"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	object := access.ItemObjectWithParent(access.SecurableDataset, "test", "sales/orders", access.ItemObject(access.SecurableSemanticModel, "test", "sales"))
	if _, err := repo.CreateGrant(ctx, access.GrantInput{Object: object, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegePreviewData}); err != nil {
		t.Fatalf("grant preview: %v", err)
	}
	expression, err := json.Marshal(map[string]any{"field": "orders.email", "mask": "redact"})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		Object:         access.ItemObjectWithParent(access.SecurableColumn, "test", "sales/orders/email", object),
		PolicyType:     "column_mask",
		ExpressionJSON: string(expression),
	}); err != nil {
		t.Fatalf("upsert policy: %v", err)
	}
	capture := &columnMaskCaptureMetrics{}
	metrics := queryauthz.New(capture, queryauthz.Options{
		Repo: repo,
		PrincipalFromContext: func(context.Context) (queryauthz.Principal, bool) {
			return queryauthz.Principal{ID: principal.ID}, true
		},
		TokenAllows: apiTokenAllows,
	})
	_, err = metrics.ExecuteDataQuery(ctx, dataquery.Query{
		WorkspaceID:   "test",
		ModelID:       "sales",
		Kind:          dataquery.KindSemanticRows,
		Target:        "orders",
		Operation:     dataquery.OperationAPIPreview,
		RequestID:     "preview_req",
		CorrelationID: "preview_corr",
		Fields:        []dataquery.Field{{Field: "orders.email", Alias: "customer_email"}},
		Limit:         1,
	})
	if err != nil {
		t.Fatalf("ExecuteDataQuery error = %v", err)
	}
	if len(capture.request.ColumnMasks) != 1 || capture.request.ColumnMasks[0].Field != "orders.email" || capture.request.ColumnMasks[0].Mask != "redact" {
		t.Fatalf("governed column masks = %#v, want orders.email redact", capture.request.ColumnMasks)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: "data_preview.executed"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1: %#v", len(events), events)
	}
	event := events[0]
	if event.PrincipalID != principal.ID || event.Privilege != access.PrivilegePreviewData || event.Status != "success" || event.RequestID != "preview_req" || event.CorrelationID != "preview_corr" {
		t.Fatalf("audit event = %#v, want preview success for principal", event)
	}
}

func TestAgentToolAuthorizationRecordsAuditEvent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_agent", Email: "agent@example.com", DisplayName: "Agent"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{Object: access.WorkspaceObject("test"), SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeQueryData}); err != nil {
		t.Fatalf("grant query: %v", err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{RequestID: "tool_call_1", CorrelationID: "agent_corr"})
	_, ok := server.routes.agentModule.VisualToolProvider().Authorize(
		ctx,
		agentmodule.ToolsScope(agentcap.Scope{WorkspaceID: "test", PrincipalID: principal.ID}),
		agenttools.VisualAuthorizationRequest{Model: "sales", Dataset: "orders", ToolName: "create_visual"},
	)
	if !ok {
		t.Fatal("authorizeAgentPrivilege ok = false, want true")
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: "agent_tool.called"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1: %#v", len(events), events)
	}
	event := events[0]
	if event.PrincipalID != principal.ID || event.TargetID != "create_visual" || event.Privilege != access.PrivilegeQueryData || event.Status != "success" || event.RequestID != "tool_call_1" || event.CorrelationID != "agent_corr" {
		t.Fatalf("audit event = %#v, want agent tool success with request metadata", event)
	}
}

type columnMaskCaptureMetrics struct {
	fakeMetrics
	request dataquery.Query
}

func (m *columnMaskCaptureMetrics) ExecuteDataQuery(_ context.Context, request dataquery.Query) (dataquery.Result, error) {
	m.request = request
	return dataquery.Result{
		Columns: dataquery.ColumnsFromNames([]string{"customer_email"}),
		Rows:    []dataquery.Row{{"customer_email": "REDACTED"}},
		Status:  dataquery.StatusSuccess,
	}, nil
}
