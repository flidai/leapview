package transport

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/pkg/pagestream"
)

type Authorize func(route, section string, next http.Handler) (http.Handler, bool)

type PageStreamConfig struct {
	Handlers  map[string]http.Handler
	Authorize Authorize
}

type PageStream struct {
	config PageStreamConfig
}

func NewPageStream(config PageStreamConfig) *PageStream {
	return &PageStream{config: config}
}

func (s *PageStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := Route(r)
	if route == "" {
		http.Error(w, "updates route is required", http.StatusBadRequest)
		return
	}
	handler := s.config.Handlers[route]
	if handler == nil {
		http.Error(w, "unknown updates route", http.StatusBadRequest)
		return
	}
	if s.config.Authorize == nil {
		handler.ServeHTTP(w, r)
		return
	}
	authorized, ok := s.config.Authorize(route, r.URL.Query().Get("section"), handler)
	if !ok || authorized == nil {
		http.Error(w, "unknown updates route", http.StatusBadRequest)
		return
	}
	authorized.ServeHTTP(w, r)
}

func Route(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get("route"))
}

// PatchOnce writes and flushes one bootstrap patch, then returns.
func PatchOnce(w http.ResponseWriter, r *http.Request, patch pagestream.SignalPatch) error {
	if _, err := EnsureClientID(w, r); err != nil {
		return err
	}
	updates := pagestream.NewSignalStream(w, r)
	return updates.Patch(patch)
}

func PatchAndWait(w http.ResponseWriter, r *http.Request, patch pagestream.SignalPatch) {
	if err := PatchOnce(w, r, patch); err != nil {
		return
	}
	<-r.Context().Done()
}
