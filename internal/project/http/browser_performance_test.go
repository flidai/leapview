package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/internal/servingstate"
)

type performanceCatalog struct{ calls int }

func (c *performanceCatalog) List(_ context.Context, r projectcatalog.ListRequest) (projectcatalog.Page, error) {
	c.calls++
	return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("model:" + r.PrincipalID), Kind: projectgraph.KindModel}}}}, nil
}

func (*performanceCatalog) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

type performanceGraph struct{ calls int }

func (g *performanceGraph) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	g.calls++
	return servingstate.AssetGraph{Assets: []servingstate.Asset{
		{ID: "model:alice", ProjectID: "project:test", Type: "model", Key: "alice", PayloadJSON: "{}"},
		{ID: "model:bob", ProjectID: "project:test", Type: "model", Key: "bob", PayloadJSON: "{}"},
	}}, true, nil
}

type performanceDefinition struct{ calls int }

func (d *performanceDefinition) ProjectDefinitionSnapshot(context.Context) (projectmanifest.ResourceManifest, map[string]*semanticquery.CompiledModel, error) {
	d.calls++
	return projectmanifest.ResourceManifest{}, nil, nil
}

func TestBrowserReadReuseIsRequestScopedAndPrincipalFiltered(t *testing.T) {
	graph, catalog, definition := &performanceGraph{}, &performanceCatalog{}, &performanceDefinition{}
	h := &BrowserHandler{
		Graph: graph, Catalog: catalog, ProjectDefinitionReader: definition,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:      func(r *http.Request) (Principal, bool) { return Principal{ID: r.Header.Get("X-Test-Principal")}, true },
	}
	for index, principal := range []string{"alice", "bob"} {
		request := httptest.NewRequest(http.MethodGet, "/explore", nil)
		request.Header.Set("X-Test-Principal", principal)
		cached := browserReadRequest(request)
		for range 2 {
			_, assets, _, err := h.loadAssets(cached)
			if err != nil || len(assets) != 1 || assets[0].ID != "model:"+principal {
				t.Fatalf("%s assets=%#v err=%v", principal, assets, err)
			}
			navigation := h.navigationCatalog(cached)
			if len(navigation.Models) != 1 || navigation.Models[0].ID != "model:"+principal {
				t.Fatalf("%s navigation=%#v", principal, navigation)
			}
			if _, _, err := h.projectDefinitionSnapshot(cached.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if graph.calls != index+1 || catalog.calls != (index+1)*2 || definition.calls != index+1 {
			t.Fatalf("repeated reads: graph=%d catalog=%d definition=%d", graph.calls, catalog.calls, definition.calls)
		}
		// The original stream request stays uncached, as required by live
		// refreshes following a serving-generation cutover.
		if _, ok := request.Context().Value(browserReadContextKey{}).(*browserRequestReads); ok {
			t.Fatal("read cache leaked into original stream request")
		}
	}
	uncached := httptest.NewRequest(http.MethodGet, "/updates", nil)
	uncached.Header.Set("X-Test-Principal", "alice")
	for range 2 {
		if _, _, _, err := h.loadAssets(uncached); err != nil {
			t.Fatal(err)
		}
	}
	if graph.calls != 4 {
		t.Fatalf("live reads reused bootstrap graph: %d", graph.calls)
	}
}

type performancePreviewExecutor struct {
	queries     []dataquery.Query
	known       bool
	total       int
	maxRows     int
	limitReason dataquery.ResultLimitReason
	failure     error
	ctx         context.Context
	cancel      context.CancelFunc
}

func (e *performancePreviewExecutor) ExecuteDataQuery(ctx context.Context, q dataquery.Query) (dataquery.Result, error) {
	if e.ctx != nil && ctx != e.ctx {
		return dataquery.Result{}, errors.New("request context changed")
	}
	e.queries = append(e.queries, q)
	if e.cancel != nil {
		e.cancel()
	}
	if e.failure != nil {
		return dataquery.Result{}, e.failure
	}
	if e.maxRows > 0 && q.Limit > e.maxRows {
		return dataquery.Result{}, fmt.Errorf("bounded query: %w", &dataquery.ResultLimitError{Reason: e.limitReason, Limit: int64(e.maxRows), Observed: int64(q.Limit)})
	}
	last := min(e.total, q.Offset+q.Limit)
	rows := make([]dataquery.Row, 0, max(0, last-q.Offset))
	for row := q.Offset; row < last; row++ {
		rows = append(rows, dataquery.Row{"id": row})
	}
	return dataquery.Result{Rows: rows, TotalRows: e.total, TotalRowsKnown: e.known && q.IncludeTotal}, nil
}

func performancePreviewObject() projectsignals.DataExplorerObjectSignal {
	return projectsignals.DataExplorerObjectSignal{Layer: "model", ResourceID: "model:orders", SemanticModelID: projectsignals.Pointer("semantic:sales"), DatasetID: projectsignals.Pointer("orders")}
}

func TestDataExplorerPreviewReadsContiguousWindowOnce(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprintf("known_%t", known), func(t *testing.T) {
			executor := &performancePreviewExecutor{known: known, total: 450}
			preview := dataExplorerPreview(t.Context(), executor, "project:test", performancePreviewObject(), projectsignals.DataExplorerCommand{Start: 350, Count: 100, Block: projectsignals.Pointer("all"), RequestSeq: 7, ResetVersion: 2})
			if preview.Error != nil || len(executor.queries) != 1 || executor.queries[0].Offset != 200 || executor.queries[0].Limit != 300 || !executor.queries[0].IncludeTotal {
				t.Fatalf("preview=%#v queries=%#v", preview, executor.queries)
			}
			for i, id := range []string{"a", "b", "c"} {
				block := preview.Blocks[id]
				wantRows := 100
				if i == 2 {
					wantRows = 50
				}
				if block.Start != int64(200+i*100) || block.RequestSeq != 7 || block.ResetVersion != 2 || len(block.Rows) != wantRows || block.Rows[0]["id"] != 200+i*100 {
					t.Fatalf("block %s = %#v", id, block)
				}
			}
			if preview.AvailableRows != 450 || preview.TotalRows != 450 {
				t.Fatalf("row extent=%d/%d", preview.AvailableRows, preview.TotalRows)
			}
		})
	}
}

func TestDataExplorerPreviewBudgetFallbackPreservesGovernedBlocks(t *testing.T) {
	for _, reason := range []dataquery.ResultLimitReason{dataquery.ResultRows, dataquery.ResultBytes} {
		t.Run(string(reason), func(t *testing.T) {
			ctx := t.Context()
			executor := &performancePreviewExecutor{ctx: ctx, known: true, total: 250, maxRows: 200, limitReason: reason}
			preview := dataExplorerPreview(ctx, executor, "project:test", performancePreviewObject(), projectsignals.DataExplorerCommand{Count: 100, Block: projectsignals.Pointer("all")})
			if preview.Error != nil || len(executor.queries) != 4 || preview.TotalRows != 250 {
				t.Fatalf("fallback=%#v queries=%#v", preview, executor.queries)
			}
			for i, q := range executor.queries[1:] {
				if q.Limit != 100 || q.Offset != i*100 || q.IncludeTotal != (i == 0) || q.ProjectID != "project:test" || q.ModelID != "semantic:sales" || q.Surface != dataquery.SurfaceDataExplorer {
					t.Fatalf("fallback query=%#v", q)
				}
			}
			if len(preview.Blocks["c"].Rows) != 50 {
				t.Fatalf("last block=%#v", preview.Blocks["c"])
			}
		})
	}
}

func TestDataExplorerPreviewDoesNotRetryUnrelatedFailureOrCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			executor := &performancePreviewExecutor{ctx: ctx, failure: errors.New("forbidden")}
			if canceled {
				executor.failure = &dataquery.ResultLimitError{Reason: dataquery.ResultRows}
				executor.cancel = cancel
			}
			preview := dataExplorerPreview(ctx, executor, "project:test", performancePreviewObject(), projectsignals.DataExplorerCommand{Count: 100, Block: projectsignals.Pointer("all")})
			if preview.Error == nil || len(executor.queries) != 1 {
				t.Fatalf("unexpected retry=%#v/%#v", preview, executor.queries)
			}
		})
	}
}
