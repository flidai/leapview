package managedrecovery

import (
	"errors"
	"strings"
	"testing"
)

func TestPostgresFailureDiagnosticsRetainOnlyBoundedCategories(t *testing.T) {
	for _, tc := range []struct{ message, category string }{
		{"FATAL: could not open /private/token: Permission denied", "permission-denied"},
		{"FATAL: could not write private-provider: Read-only file system", "read-only-filesystem"},
		{"FATAL: recovery ended before configured recovery target was reached", "recovery-target-unreached"},
		{"FATAL: bind private-endpoint: Address already in use", "address-in-use"},
		{"FATAL: data directory \"/private/cluster\" has invalid permissions", "invalid-directory-permissions"},
		{"FATAL: data directory \"/private/cluster\" has wrong ownership", "invalid-directory-owner"},
		{"FATAL: Unix-domain socket path \"/private/socket\" is too long", "unix-socket-path"},
		{"postgres: could not look up effective user ID 123: user does not exist", "user-lookup"},
		{"FATAL: invalid value for parameter \"private-setting\": \"private-value\"", "invalid-configuration"},
		{"FATAL: could not load private key file \"/private/key\"", "tls-key"},
		{"FATAL: could not map anonymous shared memory: Invalid argument", "shared-memory"},
		{"bwrap: a private mount failed", "kernel-confinement"},
		{"FATAL: password=owned-secret", "unclassified"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			var output postgresFailureOutput
			for _, value := range []string{strings.Repeat("old log", 20000), tc.message[:8], tc.message[8:]} {
				if n, err := output.Write([]byte(value)); n != len(value) || err != nil {
					t.Fatalf("diagnostic capture interfered with provider: %d %v", n, err)
				}
			}
			if len(output.tail) > 16384 {
				t.Fatal("unbounded provider output")
			}
			message := postgresProcessFailure("stopped", errors.New("password=owned-secret"), &output).Error()
			if message != "stopped ("+tc.category+")" || strings.Contains(message, "private") || strings.Contains(message, "owned-secret") {
				t.Fatalf("failure did not retain only a fixed category: %q", message)
			}
		})
	}
}
