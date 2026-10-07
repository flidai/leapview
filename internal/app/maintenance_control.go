package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"
)

var maintenanceOperationPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type maintenanceControlRequest struct {
	Revision          string `json:"revision"`
	Operation         string `json:"operation"`
	LeaseMilliseconds int64  `json:"leaseMilliseconds"`
}

// MaintenanceHandler is served only on the process-owned private Unix socket.
// Filesystem ownership is the local operator boundary; no TCP control route is
// added to the public router. Every mutation independently binds the process SHA.
func (a *Application) MaintenanceHandler() http.Handler {
	if a == nil || a.lifecycle == nil || a.lifecycle.maintenance == nil {
		return nil
	}
	g := a.lifecycle.maintenance
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(g.status())
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4097))
		decoder.DisallowUnknownFields()
		var request maintenanceControlRequest
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid maintenance request", 400)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "trailing maintenance request", 400)
			return
		}
		if request.Revision == "" || request.Revision != g.revision {
			http.Error(w, "maintenance process identity mismatch", 409)
			return
		}
		if r.URL.Path != "/close" && !maintenanceOperationPattern.MatchString(request.Operation) {
			http.Error(w, "exact maintenance operation required", 400)
			return
		}
		var err error
		switch r.URL.Path {
		case "/prepare":
			err = g.prepare(r.Context(), request.Operation)
		case "/open":
			if request.LeaseMilliseconds <= 0 || request.LeaseMilliseconds > int64((24*time.Hour)/time.Millisecond) {
				err = errors.New("bounded provisional admission lease required")
			} else {
				err = g.open(r.Context(), request.Operation, time.Duration(request.LeaseMilliseconds)*time.Millisecond)
			}
		case "/finalize":
			err = g.finalize(request.Operation)
		case "/close":
			err = g.close(r.Context())
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "maintenance operation failed: "+err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g.status())
	})
}

type readinessCapture struct {
	header http.Header
	status int
}

func (w *readinessCapture) Header() http.Header { return w.header }
func (w *readinessCapture) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(data), nil
}
func (w *readinessCapture) WriteHeader(status int) { w.status = status }
func preparedReadiness(ctx context.Context, health *health) error {
	w := &readinessCapture{header: make(http.Header)}
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/readyz", nil)
	health.Readyz(w, r)
	if w.status != http.StatusOK {
		return errors.New("prepared runtime readiness failed")
	}
	return nil
}
