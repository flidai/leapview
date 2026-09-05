package postgrestest

import (
	"crypto/x509"
	"encoding/pem"
	"os"
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

func TestConformanceSkippedParsesBooleanEnvironment(t *testing.T) {
	for _, value := range []string{"1", "true", "T", "yes", "on"} {
		t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_SKIP", value)
		if !conformanceSkipped() {
			t.Fatalf("conformanceSkipped() = false for %q", value)
		}
	}
	for _, value := range []string{"", "0", "false", "off", "no"} {
		t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_SKIP", value)
		if conformanceSkipped() {
			t.Fatalf("conformanceSkipped() = true for %q", value)
		}
	}
}

func TestRequiredOverridesConformanceSkip(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_SKIP", "1")
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	if shouldSkipConformance() {
		t.Fatal("required conformance lane would be suppressed by skip flag")
	}
}

func TestTLSCertificateFilesProducesParsablePair(t *testing.T) {
	caPath, certPath, keyPath := tlsCertificateFiles(t)
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(caPEM)
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if caBlock == nil || certBlock == nil || keyBlock == nil {
		t.Fatal("TLS certificate helper emitted incomplete PEM files")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("parse server certificate: %v", err)
	}
	if err := cert.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("server certificate is not signed by generated CA: %v", err)
	}
	if _, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err != nil {
		t.Fatalf("parse server private key: %v", err)
	}
}
