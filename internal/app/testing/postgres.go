// Package testing contains application-owned helpers shared by integration
// and conformance tests.
package testing

import (
	"os"
	"strings"

	"github.com/flidai/leapview/internal/app/config/spec"
)

// PostgresConformanceRequired reports whether the PostgreSQL conformance lane
// must fail closed when its disposable provider is unavailable. The setting
// remains part of the application environment contract; the platform harness
// receives the resolved policy explicitly.
func PostgresConformanceRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(configspec.EnvLEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}
