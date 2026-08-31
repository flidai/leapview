package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfigValidateUsesServeProfile(t *testing.T) {
	t.Setenv("LEAPVIEW_PRODUCTION", "")
	cmd := configCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"validate"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config validate: %v", err)
	}
	if got := output.String(); got != "configuration valid\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestConfigValidateDoesNotExposeSecretValues(t *testing.T) {
	secret := "short-secret-value"
	t.Setenv("LEAPVIEW_PRODUCTION", "1")
	t.Setenv("LEAPVIEW_LOCAL_AUTH", "1")
	t.Setenv("LEAPVIEW_ALLOWED_HOSTS", "leapview.example.com")
	t.Setenv("LEAPVIEW_PUBLIC_URL", "https://leapview.example.com")
	t.Setenv("LEAPVIEW_COOKIE_SECURE", "true")
	t.Setenv("LEAPVIEW_CSRF_KEY", secret)
	t.Setenv("LEAPVIEW_METRICS_BEARER_TOKEN", "0123456789abcdef0123456789abcdef")
	cmd := configCommand()
	cmd.SetArgs([]string{"validate"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("config validate accepted a short CSRF secret")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation exposed secret value: %v", err)
	}
}

func TestConfigValidateProductionFlagAppliesProductionRules(t *testing.T) {
	t.Setenv("LEAPVIEW_PRODUCTION", "")
	cmd := configCommand()
	cmd.SetArgs([]string{"validate", "--production"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("config validate --production accepted missing production authentication")
	}
}

func TestConfigValidateProductionRequiresPostgresContract(t *testing.T) {
	t.Setenv("LEAPVIEW_PRODUCTION", "")
	t.Setenv("LEAPVIEW_LOCAL_AUTH", "1")
	t.Setenv("LEAPVIEW_ALLOWED_HOSTS", "leapview.example.com")
	t.Setenv("LEAPVIEW_PUBLIC_URL", "https://leapview.example.com")
	t.Setenv("LEAPVIEW_COOKIE_SECURE", "true")
	t.Setenv("LEAPVIEW_CSRF_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("LEAPVIEW_METRICS_BEARER_TOKEN", "0123456789abcdef0123456789abcdef")
	cmd := configCommand()
	cmd.SetArgs([]string{"validate", "--production"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_URL") {
		t.Fatalf("production PostgreSQL contract error = %v", err)
	}
}
