package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMaintenanceNeverStartsWorkBeforePreparedAuthorizedOpen(t *testing.T) {
	var work atomic.Int32
	gate := newMaintenanceAdmission("revision", func(context.Context) error { return nil }, func(context.Context) error { work.Add(1); return nil }, func(context.Context) error { return nil })
	if err := gate.open(t.Context(), "operation", time.Second); err == nil {
		t.Fatal("opened without preparation")
	}
	if err := gate.prepare(t.Context(), "operation"); err != nil {
		t.Fatal(err)
	}
	if work.Load() != 0 {
		t.Fatal("preparation performed product work")
	}
	handler := gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), func(context.Context) error { return nil })
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/product", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if err := gate.open(t.Context(), "operation", time.Second); err != nil {
		t.Fatal(err)
	}
	if work.Load() != 1 {
		t.Fatal("authorized open did not start workers")
	}
	if err := gate.finalize("operation"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/product", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
func TestMaintenancePartialStartAndLeaseExpiryCloseWork(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner loss", true: "partial start failure"}[fail], func(t *testing.T) {
			stopped := make(chan struct{}, 1)
			gate := newMaintenanceAdmission("revision", func(context.Context) error { return nil }, func(context.Context) error {
				if fail {
					return errors.New("second worker failed")
				}
				return nil
			}, func(context.Context) error {
				select {
				case stopped <- struct{}{}:
				default:
				}
				return nil
			})
			if err := gate.prepare(t.Context(), "operation"); err != nil {
				t.Fatal(err)
			}
			err := gate.open(t.Context(), "operation", 20*time.Millisecond)
			if (err != nil) != fail {
				t.Fatal(err)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("lost runner left worker admission open")
			}
			if got := gate.status().State; got != "failed" && got != "closed" {
				t.Fatal(got)
			}
			if gate.finalize("operation") == nil {
				t.Fatal("finalized closed admission")
			}
		})
	}
}

func TestMaintenanceCloseCancelsStreamsAndDrainsAcknowledgedWrites(t *testing.T) {
	gate := newMaintenanceAdmission("revision", func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error { return nil })
	if err := gate.prepare(t.Context(), "operation"); err != nil {
		t.Fatal(err)
	}
	if err := gate.open(t.Context(), "operation", time.Minute); err != nil {
		t.Fatal(err)
	}
	defer gate.close(context.Background())
	streamEntered, streamDone := make(chan struct{}), make(chan struct{})
	writeEntered, writeRelease, writeDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var acknowledged atomic.Bool
	handler := gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/updates" {
			close(streamEntered)
			<-r.Context().Done()
			close(streamDone)
			return
		}
		close(writeEntered)
		<-writeRelease
		acknowledged.Store(true)
		w.WriteHeader(204)
		close(writeDone)
	}), func(context.Context) error { return nil })
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/updates", nil))
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/upload", nil))
	<-streamEntered
	<-writeEntered
	closed := make(chan error, 1)
	go func() { closed <- gate.close(t.Context()) }()
	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Fatal("SSE did not drain")
	}
	select {
	case err := <-closed:
		t.Fatalf("closed before admitted write completed: %v", err)
	default:
	}
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest("POST", "http://localhost/new-write", nil))
	if denied.Code != 503 {
		t.Fatal(denied.Code)
	}
	close(writeRelease)
	<-writeDone
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !acknowledged.Load() {
		t.Fatal("admitted write was discarded")
	}
	if gate.prepare(t.Context(), "operation") == nil {
		t.Fatal("reused stopped process instead of exclusive restart")
	}
}

func TestMaintenanceCloseWinsConcurrentPreparation(t *testing.T) {
	for _, during := range []string{"prepare", "open"} {
		t.Run(during, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var starts atomic.Int32
			gate := newMaintenanceAdmission("revision", func(context.Context) error { return nil }, func(context.Context) error { starts.Add(1); return nil }, func(context.Context) error { return nil })
			if during == "open" {
				if err := gate.prepare(t.Context(), "operation"); err != nil {
					t.Fatal(err)
				}
			}
			gate.prepareWorkers = func(context.Context) error { close(entered); <-release; return nil }
			result := make(chan error, 1)
			go func() {
				if during == "prepare" {
					result <- gate.prepare(t.Context(), "operation")
				} else {
					result <- gate.open(t.Context(), "operation", time.Second)
				}
			}()
			<-entered
			closed := make(chan error, 1)
			go func() { closed <- gate.close(t.Context()) }()
			deadline := time.Now().Add(time.Second)
			for gate.status().State != "draining" {
				if time.Now().After(deadline) {
					t.Fatal("close did not mark admission")
				}
				time.Sleep(time.Millisecond)
			}
			close(release)
			if err := <-result; err == nil {
				t.Fatal("preparation reopened closed admission")
			}
			if err := <-closed; err != nil {
				t.Fatal(err)
			}
			if starts.Load() != 0 || !gate.status().Drained || gate.status().State != "closed" {
				t.Fatal(gate.status(), starts.Load())
			}
			if gate.open(t.Context(), "operation", time.Second) == nil {
				t.Fatal("terminal process reopened")
			}
		})
	}
}
