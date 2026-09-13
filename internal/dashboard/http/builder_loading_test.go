package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
)

type loadingBuilderFake struct {
	*builderAuthoringFake
	beforePreview func()
}

func (f *loadingBuilderFake) Preview(ctx context.Context, request preview.PreviewRequest) (preview.Preview, error) {
	f.beforePreview()
	return f.builderAuthoringFake.Preview(ctx, request)
}

func TestBuilderNavigationShellFlushesBeforePreview(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	fake := &loadingBuilderFake{builderAuthoringFake: &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{
		DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue",
		Capabilities: uisignals.DashboardBuilderCapabilitiesSignal{CanEdit: true, CanAddVisual: true},
	}}}
	fake.beforePreview = func() {
		if !rec.Flushed {
			t.Fatal("navigation shell was not flushed before data query")
		}
		patches := ssetest.PatchSignals(t, rec.Body.String())
		if len(patches) != 1 {
			t.Fatalf("initial patches = %d", len(patches))
		}
		builder := patches[0]["builder"].(map[string]any)
		if builder["capabilities"].(map[string]any)["canEdit"] != false || builder["preview"].(map[string]any)["loading"] != true {
			t.Fatalf("unsafe loading shell: %#v", builder)
		}
		if !fake.builder.Capabilities.CanEdit {
			t.Fatal("loading shell mutated authoritative capabilities")
		}
		cancel()
	}
	h := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "principal-1" }}
	h.DashboardBuilderUpdates(rec, httptest.NewRequest(http.MethodGet, "/updates?dashboard=revenue&draft=draft-7&page=overview", nil).WithContext(ctx))
	if fake.previewCalls != 1 {
		t.Fatalf("preview calls = %d, body=%s", fake.previewCalls, rec.Body.String())
	}
}
