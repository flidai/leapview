package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
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

func TestDashboardBuilderRefreshPreservesAgentSignals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	bootstrapCalls := 0
	fake := &loadingBuilderFake{builderAuthoringFake: &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{
		DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue",
		Capabilities: uisignals.DashboardBuilderCapabilitiesSignal{CanEdit: true, CanAddVisual: true},
	}}}
	fake.beforePreview = func() {}
	h := Handler{
		Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
		AgentBootstrap: func(*http.Request, string) reportui.AgentBootstrap {
			bootstrapCalls++
			return reportui.AgentBootstrap{}
		},
	}
	currentSignals := `{"agent":{"activeConversationId":"conversation-1","transcript":[{"id":"answer-1","kind":"assistant","text":"Keep me"}]},"agentVisuals":{"chart":{"title":"Current result"}}}`
	request := httptest.NewRequest(http.MethodGet, "/updates?dashboard=revenue&draft=draft-7&route=dashboard_builder&snapshot=1&datastar="+url.QueryEscape(currentSignals), nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.DashboardBuilderUpdates(rec, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("snapshot refresh kept the SSE connection open")
	}

	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 2 {
		t.Fatalf("builder refresh patches = %d, want preview reset then canonical bootstrap: %s", len(patches), rec.Body.String())
	}
	if previews, ok := patches[0]["builderVisuals"]; !ok || previews != nil {
		t.Fatalf("builder refresh did not clear old visual envelope: %#v", patches[0])
	}
	if _, loading := patches[0]["status"]; loading {
		t.Fatalf("builder refresh showed a loading shell: %#v", patches[0])
	}
	bootstrap := patches[len(patches)-1]
	if builder, ok := bootstrap["builder"].(map[string]any); !ok || builder["title"] != "Revenue" {
		t.Fatalf("canonical builder bootstrap = %#v", bootstrap["builder"])
	}
	if _, exists := bootstrap["agent"]; exists {
		t.Fatalf("builder refresh replaced agent transcript: %#v", bootstrap["agent"])
	}
	if _, exists := bootstrap["agentVisuals"]; exists {
		t.Fatalf("builder refresh replaced agent visuals: %#v", bootstrap["agentVisuals"])
	}
	if bootstrapCalls != 0 {
		t.Fatalf("AgentBootstrap calls = %d, want 0 when client agent state is present", bootstrapCalls)
	}
}
