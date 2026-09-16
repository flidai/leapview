package transport

import (
	"net/http"
	"reflect"
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
	if _, err := EnsureClientID(w, r); err != nil {
		return
	}
	updates := pagestream.NewSignalStream(w, r)
	if err := updates.Patch(patch); err != nil {
		return
	}
	updates.Wait(r.Context())
}

// PatchAndWatch rereads an authoritative page after a committed-change hint.
// The caller subscribes before constructing bootstrap, closing the gap between
// its first read and the live stream. Only changed read models are pushed.
func PatchAndWatch(w http.ResponseWriter, r *http.Request, bootstrap pagestream.SignalPatch, wake <-chan pagestream.SignalPatch, read func() (pagestream.SignalPatch, error)) {
	if _, err := EnsureClientID(w, r); err != nil {
		return
	}
	updates := pagestream.NewSignalStream(w, r)
	if err := updates.Patch(bootstrap); err != nil {
		return
	}
	if wake == nil || read == nil {
		updates.Wait(r.Context())
		return
	}
	mailbox := make(chan pagestream.SignalPatch, 1)
	go func() {
		defer close(mailbox)
		last := pagestream.SignalPatch{"page": bootstrap["page"]}
		for {
			select {
			case <-r.Context().Done():
				return
			case _, ok := <-wake:
				if !ok {
					return
				}
				patch, err := read()
				if err != nil {
					// Do not remain indefinitely stale when an invalidation read
					// fails. Closing this SSE stream lets the browser reconnect
					// and bootstrap from the authoritative state again.
					return
				}
				if reflect.DeepEqual(last, patch) {
					continue
				}
				select {
				case mailbox <- patch:
					last = patch
				case <-r.Context().Done():
					return
				}
			}
		}
	}()
	_ = updates.ForwardUpdates(r.Context(), mailbox)
}
