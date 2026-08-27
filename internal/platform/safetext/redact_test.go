package safetext

import (
	"strings"
	"testing"
)

func TestBoundedSummaryRemovesCredentialRepresentations(t *testing.T) {
	privateKeyBlock := "-----BEGIN " + "PRIVATE KEY-----\nPEM-SECRET-MATERIAL\n-----END " + "PRIVATE KEY-----"
	input := strings.Join([]string{
		`postgres://user:hunter2@db.example/prod`,
		`{"secret":"json-secret","safe":"visible"}`,
		`https://s3.example/object?X-Amz-Credential=access&X-Amz-Signature=signed`,
		`AWS_SECRET_ACCESS_KEY=provider-secret`,
		`Authorization: Bearer bearer-secret`,
		`Authorization: Basic dXNlcjpzdXBlci1zZWNyZXQ=`,
		`AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE`,
		`AccountKey=azure-account-key`,
		`https://blob.example/object?sv=2024&sig=azure-sas-signature`,
		`{"private_key":"json-private-key"}`,
		privateKeyBlock,
		`Server=db;Uid=admin;Pwd=dsn-password`,
		"second\nline",
	}, " ")
	got := BoundedSummary(input, 512)
	for _, secret := range []string{"hunter2", "json-secret", "access", "signed", "provider-secret", "bearer-secret", "dXNlc", "AKIAIOS", "azure-account-key", "azure-sas-signature", "json-private-key", "PEM-SECRET", "dsn-password"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized text contains %q: %s", secret, got)
		}
	}
	if strings.Contains(got, "\n") || !strings.Contains(got, "visible") {
		t.Fatalf("sanitized summary = %q", got)
	}
}

func TestCredentialsRedactsProviderAndStructuredSecrets(t *testing.T) {
	privateKeyBlock := "failure: -----BEGIN " + "PRIVATE KEY-----\nPEM-SECRET-MATERIAL\n-----END " + "PRIVATE KEY-----"
	tests := map[string]struct {
		input  string
		secret string
	}{
		"pem":               {privateKeyBlock, "PEM-SECRET-MATERIAL"},
		"json private key":  {`{"private_key":"json-private-key"}`, "json-private-key"},
		"aws access key":    {`AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE`, "AKIAIOSFODNN7EXAMPLE"},
		"aws signature":     {`https://s3.example/o?X-Amz-Credential=scope&X-Amz-Signature=aws-signature`, "aws-signature"},
		"azure account key": {`AccountKey=azure-account-key`, "azure-account-key"},
		"azure sas":         {`https://blob.example/o?sv=2024&sig=azure-sas-signature`, "azure-sas-signature"},
		"basic auth":        {`Authorization: Basic dXNlcjpzdXBlci1zZWNyZXQ=`, "dXNlcjpzdXBlci1zZWNyZXQ="},
		"dsn":               {`Server=db;Uid=admin;Pwd=dsn-password`, "dsn-password"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := BoundedSummary(test.input, 1024)
			if strings.Contains(got, test.secret) || !strings.Contains(got, Replacement) {
				t.Fatalf("sanitized summary = %q", got)
			}
		})
	}
}
