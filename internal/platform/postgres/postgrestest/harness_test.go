package postgrestest

import (
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

func TestRequiredParsesBooleanEnvironment(t *testing.T) {
	for _, value := range []string{"1", "true", "T", "yes", "on"} {
		t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", value)
		if !Required() {
			t.Fatalf("Required() = false for %q", value)
		}
	}
	for _, value := range []string{"", "0", "false", "off", "no"} {
		t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", value)
		if Required() {
			t.Fatalf("Required() = true for %q", value)
		}
	}
}
