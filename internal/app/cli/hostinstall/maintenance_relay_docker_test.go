//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestInternalNetworkRecoveryRelay(t *testing.T) {
	if os.Getenv("LEAPVIEW_HOST_UPGRADE_QUALIFICATION") != "1" {
		t.Skip("explicit disposable Docker qualification required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	name := "leapview-relay-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	docker := func(args ...string) string {
		t.Helper()
		raw, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("Docker %s: %s %v", args[0], raw, err)
		}
		return strings.TrimSpace(string(raw))
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "-v", name).Run()
		_ = exec.Command("docker", "network", "rm", name).Run()
	})
	docker("network", "create", "--internal", name)
	docker("run", "-d", "--name", name, "--network", name, "-e", "POSTGRES_PASSWORD=isolated-relay-test", nativePGImage)
	for attempt := 0; attempt < 100; attempt++ {
		if exec.CommandContext(ctx, "docker", "exec", name, "pg_isready", "-h", "127.0.0.1").Run() == nil {
			break
		}
		if attempt == 99 {
			t.Fatal("isolated PostgreSQL not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}

	var records []struct {
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
			Ports    map[string]any
		}
	}
	if err := json.Unmarshal([]byte(docker("inspect", name)), &records); err != nil || len(records) != 1 {
		t.Fatalf("inspect: %v", err)
	}
	published := false
	for _, bindings := range records[0].NetworkSettings.Ports {
		published = published || bindings != nil
	}
	if published {
		t.Fatal("isolated clone unexpectedly published ports")
	}
	ip := records[0].NetworkSettings.Networks[name].IPAddress
	address, closeRelay, err := startTCPRelay(ctx, "127.0.0.1:0", net.JoinHostPort(ip, "5432"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	connection, err := pgx.Connect(ctx, "postgres://postgres:isolated-relay-test@"+address+"/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	var proof int
	if err = connection.QueryRow(ctx, "SELECT 741").Scan(&proof); err != nil || proof != 741 {
		t.Fatalf("private relay query: %d %v", proof, err)
	}
}
