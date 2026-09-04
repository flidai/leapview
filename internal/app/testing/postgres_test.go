package testing

import (
	stdtesting "testing"

	"github.com/flidai/leapview/internal/app/config/spec"
)

func TestPostgresConformanceRequiredParsesTruthyValues(t *stdtesting.T) {
	for _, value := range []string{"1", "true", "TRUE", "t", "yes", "YES", "on", " On "} {
		t.Setenv(configspec.EnvLEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED, value)
		if !PostgresConformanceRequired() {
			t.Errorf("PostgresConformanceRequired() = false for %q, want true", value)
		}
	}
}

func TestPostgresConformanceRequiredDefaultsFalse(t *stdtesting.T) {
	for _, value := range []string{"", "0", "false", "FALSE", "f", "no", "off", "maybe"} {
		t.Setenv(configspec.EnvLEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED, value)
		if PostgresConformanceRequired() {
			t.Errorf("PostgresConformanceRequired() = true for %q, want false", value)
		}
	}
}
