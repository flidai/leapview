package transport

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/pkg/pagestream"
)

func TestPatchAndWatchPushesChangedPageOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "/updates?route=pipelines", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	reads := 0
	wake := make(chan pagestream.SignalPatch, 3)
	wake <- pagestream.SignalPatch{"refreshChanged": true}
	wake <- pagestream.SignalPatch{"refreshChanged": true}
	wake <- pagestream.SignalPatch{"refreshChanged": true}
	close(wake)
	PatchAndWatch(w, r, pagestream.SignalPatch{"page": map[string]any{"status": "queued"}}, wake, func() (pagestream.SignalPatch, error) {
		reads++
		status := "queued"
		if reads >= 2 {
			status = "succeeded"
		}
		return pagestream.SignalPatch{"page": map[string]any{"status": status}}, nil
	})
	body := w.Body.String()
	if strings.Count(body, `"status":"queued"`) != 1 || strings.Count(body, `"status":"succeeded"`) != 1 {
		t.Fatalf("SSE patches = %q, want one bootstrap and one changed page", body)
	}
}

func TestPatchAndWatchDoesNotReadWithoutNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "/updates?route=pipelines", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	reads := 0
	PatchAndWatch(w, r, pagestream.SignalPatch{"page": map[string]any{"status": "queued"}}, make(chan pagestream.SignalPatch), func() (pagestream.SignalPatch, error) {
		reads++
		return nil, nil
	})
	if reads != 0 {
		t.Fatalf("read %d times without a notification", reads)
	}
}
