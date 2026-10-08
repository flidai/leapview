package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceControlBindsRevisionAndOperation(t *testing.T) {
	gate := newMaintenanceAdmission("revision", func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error { return nil })
	application := &Application{lifecycle: &applicationLifecycleOwner{maintenance: gate}}
	handler := application.MaintenanceHandler()
	operation := "sha256:" + strings.Repeat("a", 64)
	invoke := func(path, revision, operation string) int {
		raw, _ := json.Marshal(maintenanceControlRequest{Revision: revision, Operation: operation, LeaseMilliseconds: 1000})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(raw)))
		return w.Code
	}
	if invoke("/prepare", "other", operation) != 409 {
		t.Fatal("accepted wrong process revision")
	}
	if invoke("/prepare", "revision", "wrong-operation") != 400 {
		t.Fatal("accepted unbound operation")
	}
	if invoke("/prepare", "revision", operation) != 200 {
		t.Fatal("preparation failed")
	}
	if invoke("/open", "revision", "sha256:"+strings.Repeat("b", 64)) != 409 {
		t.Fatal("opened different operation")
	}
	if invoke("/open", "revision", operation) != 200 {
		t.Fatal("open failed")
	}
	defer gate.close(context.Background())
	if invoke("/finalize", "revision", "sha256:"+strings.Repeat("b", 64)) != 409 {
		t.Fatal("finalized different operation")
	}
}

func TestMaintenanceControllerKilledAfterOpenExpiresWork(t *testing.T) {
	if os.Getenv("LEAPVIEW_MAINTENANCE_CONTROLLER_HELPER") == "1" {
		socket := os.Getenv("LEAPVIEW_MAINTENANCE_CONTROLLER_SOCKET")
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		client := &http.Client{Transport: transport, Timeout: time.Second * 5}
		for _, action := range []string{"prepare", "open"} {
			raw, _ := json.Marshal(maintenanceControlRequest{Revision: "revision", Operation: "sha256:" + strings.Repeat("a", 64), LeaseMilliseconds: 2000})
			response, err := client.Post("http://maintenance/"+action, "application/json", bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode)
			}
		}
		fmt.Println("OPENED")
		var wait [1]byte
		_, _ = os.Stdin.Read(wait[:])
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix maintenance control")
	}
	dir, err := os.MkdirTemp("", "lv-maint-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{}, 1)
	gate := newMaintenanceAdmission("revision", func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error {
		select {
		case stopped <- struct{}{}:
		default:
		}
		return nil
	})
	server := &http.Server{Handler: (&Application{lifecycle: &applicationLifecycleOwner{maintenance: gate}}).MaintenanceHandler()}
	go server.Serve(listener)
	defer server.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestMaintenanceControllerKilledAfterOpenExpiresWork$")
	command.Env = append(os.Environ(), "LEAPVIEW_MAINTENANCE_CONTROLLER_HELPER=1", "LEAPVIEW_MAINTENANCE_CONTROLLER_SOCKET="+listener.Addr().String())
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "OPENED\n" {
		t.Fatalf("controller=%q error=%v stderr=%s", line, err, stderr.String())
	}
	if gate.status().State != "provisional" {
		t.Fatal("controller did not establish provisional work")
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("hard-killed controller left work admitted")
	}
	// The stop callback signals before close publishes its terminal status.
	// Observe completed shutdown rather than racing that final state update.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := gate.status()
		if status.State == "failed" && status.Drained {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expired maintenance did not finish draining: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
}
