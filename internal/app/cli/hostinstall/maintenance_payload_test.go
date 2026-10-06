package hostinstall

import (
	"os"
	"strings"
	"testing"
)

func TestMaintenancePayloadTransition(t *testing.T) {
	compose, err := os.ReadFile("../../../../deploy/compose/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current := string(compose)
	previous := strings.Replace(current, "[CMD, /usr/local/bin/leapview, healthcheck]", "[CMD, leapview, healthcheck]", 1)
	if previous == current {
		t.Fatal("fixture did not change the healthcheck command")
	}
	payload := func(compose string) map[string][]byte {
		return map[string][]byte{
			"compose.yaml": []byte(compose), "compose.https.yaml": []byte("proxy"),
			"compose.postgres.yaml":          []byte("postgres overlay"),
			"postgres/bundled-entrypoint.sh": []byte("entrypoint"), "postgres/bundled-init.sh": []byte("initializer"),
			"Caddyfile": []byte("caddy"), "deployment.env.example": []byte("settings"),
		}
	}
	tests := []struct {
		name      string
		change    func(map[string][]byte, map[string][]byte, map[string][]byte)
		wantError bool
	}{
		{"canonical healthcheck transition", nil, false},
		{"unchanged current payload", func(i, p, c map[string][]byte) {
			i["compose.yaml"] = c["compose.yaml"]
			p["compose.yaml"] = c["compose.yaml"]
		}, false},
		{"installed drift", func(i, p, c map[string][]byte) { i["compose.yaml"] = c["compose.yaml"] }, true},
		{"changed public binding", func(i, p, c map[string][]byte) {
			c["compose.yaml"] = []byte(strings.Replace(current, "127.0.0.1:8080", "0.0.0.0:8080", 1))
		}, true},
		{"changed volume", func(i, p, c map[string][]byte) {
			c["compose.yaml"] = []byte(strings.Replace(current, "leapview-state:/var/lib/leapview", "other-state:/var/lib/leapview", 1))
		}, true},
		{"changed health interval", func(i, p, c map[string][]byte) {
			c["compose.yaml"] = []byte(strings.Replace(current, "interval: 30s", "interval: 300s", 1))
		}, true},
		{"shell healthcheck", func(i, p, c map[string][]byte) {
			c["compose.yaml"] = []byte(strings.Replace(current, "[CMD, /usr/local/bin/leapview, healthcheck]", "[CMD-SHELL, 'true']", 1))
		}, true},
		{"missing healthcheck", func(i, p, c map[string][]byte) {
			c["compose.yaml"] = []byte(strings.Replace(current, "healthcheck:", "other:", 1))
		}, true},
		{"duplicate services", func(i, p, c map[string][]byte) { c["compose.yaml"] = []byte(current + "\nservices: {}\n") }, true},
		{"malformed YAML", func(i, p, c map[string][]byte) { c["compose.yaml"] = []byte("services: [") }, true},
		{"proxy changes", func(i, p, c map[string][]byte) { c["Caddyfile"] = []byte("changed") }, true},
		{"missing predecessor evidence", func(i, p, c map[string][]byte) { delete(p, "compose.yaml") }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i, p, c := payload(previous), payload(previous), payload(current)
			if tc.change != nil {
				tc.change(i, p, c)
			}
			err := validateMaintenancePayloadTransition(i, p, c)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

func TestRevision019MaintenancePayloadTransitionAdmitsBundledAdapterOnce(t *testing.T) {
	compose, err := os.ReadFile("../../../../deploy/compose/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current := string(compose)
	previous := strings.Replace(current, "[CMD, /usr/local/bin/leapview, healthcheck]", "[CMD, leapview, healthcheck]", 1)
	if previous == current {
		t.Fatal("fixture did not model the exact predecessor healthcheck")
	}
	payload := func(compose string) map[string][]byte {
		return map[string][]byte{
			"compose.yaml": []byte(compose), "compose.https.yaml": []byte("proxy"),
			"Caddyfile": []byte("caddy"), "deployment.env.example": []byte("settings"),
		}
	}
	installed, predecessor, candidate := payload(previous), payload(previous), payload(current)
	for name, contents := range map[string][]byte{
		"compose.postgres.yaml":          []byte("bundled topology"),
		"postgres/bundled-entrypoint.sh": []byte("entrypoint"),
		"postgres/bundled-init.sh":       []byte("initializer"),
	} {
		candidate[name] = contents
	}
	if err := validateRevision019MaintenancePayloadTransition(installed, predecessor, candidate); err != nil {
		t.Fatalf("exact revision 019 transition rejected: %v", err)
	}
	delete(candidate, "postgres/bundled-init.sh")
	if err := validateRevision019MaintenancePayloadTransition(installed, predecessor, candidate); err == nil {
		t.Fatal("revision 019 transition accepted an incomplete bundled adapter payload")
	}
}
