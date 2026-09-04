package postgrestest

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateIdentifier(t *testing.T) {
	for _, test := range []struct {
		name  string
		valid bool
	}{
		{name: "conformance", valid: true},
		{name: "_private", valid: true},
		{name: "", valid: false},
		{name: "has-hyphen", valid: false},
		{name: "1starts_with_digit", valid: false},
		{name: strings.Repeat("x", 64), valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateIdentifier(test.name); (err == nil) != test.valid {
				t.Fatalf("validateIdentifier(%q) error = %v, valid = %t", test.name, err, test.valid)
			}
		})
	}
}

func TestValidateRoleRequiresPasswordForLogin(t *testing.T) {
	if err := validateRole(Role{Name: "runtime", Login: true}); err == nil {
		t.Fatal("LOGIN role without password unexpectedly accepted")
	}
	if err := validateRole(Role{Name: "runtime", Login: true, Password: "secret"}); err != nil {
		t.Fatalf("LOGIN role with password rejected: %v", err)
	}
	if err := validateRole(Role{Name: "owner"}); err != nil {
		t.Fatalf("NOLOGIN role rejected: %v", err)
	}
}

type startupFailureRecorder struct {
	fatal bool
	skip  bool
	text  string
}

func (r *startupFailureRecorder) Fatalf(format string, args ...any) {
	r.fatal = true
	r.text = fmt.Sprintf(format, args...)
}

func (r *startupFailureRecorder) Skipf(format string, args ...any) {
	r.skip = true
	r.text = fmt.Sprintf(format, args...)
}

func TestReportStartupFailureSkipsOptionalUnavailableProvider(t *testing.T) {
	reporter := new(startupFailureRecorder)
	reportStartupFailure(reporter, false, fmt.Errorf("provider unavailable"))
	if !reporter.skip || reporter.fatal {
		t.Fatalf("optional startup failure = %#v, want skip without fatal", reporter)
	}
	if !strings.Contains(reporter.text, "provider unavailable") {
		t.Fatalf("optional startup failure message = %q, want provider error", reporter.text)
	}
}

func TestReportStartupFailureFatalsRequiredUnavailableProvider(t *testing.T) {
	reporter := new(startupFailureRecorder)
	reportStartupFailure(reporter, true, fmt.Errorf("provider unavailable"))
	if !reporter.fatal || reporter.skip {
		t.Fatalf("required startup failure = %#v, want fatal without skip", reporter)
	}
	if !strings.Contains(reporter.text, "required PostgreSQL 18 conformance container") || !strings.Contains(reporter.text, "provider unavailable") {
		t.Fatalf("required startup failure message = %q, want required provider error", reporter.text)
	}
}
