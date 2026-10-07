// Disposable transport protocol fixture. This is not LeapView and does not
// qualify application/database admission, jobs, credentials, or production use.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

var revision string

func main() {
	home := os.Getenv("LEAPVIEW_HOME")
	socket := os.Getenv("LEAPVIEW_MAINTENANCE_SOCKET")
	if home == "" || socket == "" {
		panic("fixture home/socket required")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		panic(err)
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		panic(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		panic(err)
	}
	var mu sync.Mutex
	state, operation := "closed", ""
	closed := make(chan struct{})
	control := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			var body struct{ Revision, Operation string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Revision != revision {
				http.Error(w, "identity", 409)
				return
			}
			operation = body.Operation
			switch r.URL.Path {
			case "/prepare":
				state = "prepared"
			case "/open":
				state = "provisional"
			case "/finalize":
				state = "admitted"
			case "/close":
				if state != "closed" {
					close(closed)
					closed = make(chan struct{})
				}
				state = "closed"
			default:
				http.NotFound(w, r)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": revision, "operation": operation, "state": state, "drained": state == "closed"})
	})
	go func() {
		if err := http.Serve(listener, control); err != nil {
			panic(err)
		}
	}()
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/maintenance/readyz" {
			w.WriteHeader(200)
			return
		}
		mu.Lock()
		admitted, done := state == "admitted" || state == "provisional", closed
		mu.Unlock()
		if !admitted {
			http.Error(w, "closed", 503)
			return
		}
		switch r.URL.Path {
		case "/updates":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: transport-fixture\ndata: %s\n\n", revision)
			w.(http.Flusher).Flush()
			select {
			case <-done:
			case <-r.Context().Done():
			}
		case "/write":
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			data, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if err != nil {
				http.Error(w, "read", 500)
				return
			}
			file, err := os.OpenFile(filepath.Join(home, "acknowledged-writes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				http.Error(w, "open", 500)
				return
			}
			_, err = file.Write(data)
			if err == nil {
				err = file.Sync()
			}
			_ = file.Close()
			if err != nil {
				http.Error(w, "write", 500)
				return
			}
			_, _ = fmt.Fprint(w, revision)
		case "/read":
			data, err := os.ReadFile(filepath.Join(home, "acknowledged-writes"))
			if err != nil {
				http.Error(w, "read", 500)
				return
			}
			_, _ = w.Write(data)
		default:
			_, _ = fmt.Fprint(w, revision)
		}
	})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-stop; os.Exit(0) }()
	if err := http.ListenAndServe(":8080", public); err != nil {
		panic(err)
	}
}
