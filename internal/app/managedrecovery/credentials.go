package managedrecovery

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/jackc/pgx/v5"
)

// ManagedCredentials is a private managed-local document. It has no remote
// object-store fields and cannot be consumed as the existing v2 bundle.
// KeyringPath identifies retained private key material; admission must check
// its digest and the existing credential module's durable instance owner.
type ManagedCredentials struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Profile        string `json:"profile"`
	RecoverySetID  string `json:"recoverySetId"`
	TargetID       string `json:"targetId"`
	OccurrenceID   string `json:"occurrenceId"`
	ControlURL     string `json:"controlUrl"`
	DuckLakeURL    string `json:"duckLakeUrl"`
	PostgresRootCA string `json:"postgresRootCa"`
	InstanceID     string `json:"instanceId"`
	KeyringPath    string `json:"keyringPath"`
	KeyringDigest  string `json:"keyringDigest"`
}

type RuntimeRoles struct {
	Control  string
	DuckLake string
}

func (credentials ManagedCredentials) ValidateForHandoff(handoff providerrestore.ReplacementHandoff, roles RuntimeRoles) error {
	if credentials.SchemaVersion != 1 || credentials.Profile != providerrestore.ManagedLocalProfile || handoff.SchemaVersion != providerrestore.ManagedLocalHandoffSchemaVersion || handoff.ManagedLocal == nil || handoff.ManagedLocal.Profile != credentials.Profile || credentials.RecoverySetID != handoff.RecoverySetID || credentials.TargetID != handoff.TargetID || credentials.OccurrenceID != handoff.ManagedLocal.OccurrenceID || credentials.RecoverySetID == "" || credentials.TargetID == "" || credentials.OccurrenceID == "" || credentials.InstanceID == "" || !filepath.IsAbs(credentials.KeyringPath) || filepath.Clean(credentials.KeyringPath) != credentials.KeyringPath || credentials.KeyringPath == "/" || !validContentDigest(credentials.KeyringDigest) {
		return errors.New("managed credentials differ from the exact local recovery occurrence")
	}
	if _, err := postgresRoots(credentials.PostgresRootCA); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, endpoint := range handoff.Providers {
		var value, role, key string
		switch endpoint.Role {
		case "control":
			value, role, key = credentials.ControlURL, roles.Control, "postgres.control.url"
		case "ducklake":
			value, role, key = credentials.DuckLakeURL, roles.DuckLake, "postgres.ducklake.url"
		default:
			return errors.New("managed credentials reject remote provider roles")
		}
		parsed, err := managedPostgresURL(value, role)
		if err != nil {
			return err
		}
		database := strings.TrimPrefix(parsed.Path, "/")
		parsed.User, parsed.Path, parsed.RawPath = nil, "", ""
		if seen[endpoint.Role] || parsed.String() != endpoint.Endpoint || database != endpoint.Database || endpoint.CredentialSecretKey != key || endpoint.TLSRootCASecretKey != "postgres.root-ca" {
			return errors.New("managed runtime credential differs from its retained endpoint")
		}
		seen[endpoint.Role] = true
	}
	if len(seen) != 2 {
		return errors.New("both managed runtime database roles required")
	}
	return nil
}

func validContentDigest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && strings.Trim(value[7:], "0123456789abcdef") == ""
}

func managedPostgresURL(value, expectedRole string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "postgres" || parsed.User == nil || parsed.User.Username() != expectedRole || expectedRole == "" || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawPath != "" || len(parsed.Query()) != 1 || len(parsed.Query()["sslmode"]) != 1 || parsed.Query().Get("sslmode") != "verify-full" || len(parsed.Path) < 2 || strings.Contains(parsed.Path[1:], "/") || strings.ContainsAny(expectedRole, "\r\n\x00") {
		return nil, errors.New("explicit TLS managed runtime URL required")
	}
	password, present := parsed.User.Password()
	if !present || password == "" {
		return nil, errors.New("explicit runtime password required; ambient credentials forbidden")
	}
	if port := parsed.Port(); port != "" {
		integer, err := strconv.Atoi(port)
		if err != nil || integer < 1 || integer > 65535 {
			return nil, errors.New("invalid managed PostgreSQL port")
		}
	}
	return parsed, nil
}

func postgresRoots(raw string) (*x509.CertPool, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return nil, errors.New("bounded explicit PostgreSQL CA required")
	}
	pool, count := x509.NewCertPool(), 0
	for strings.TrimSpace(raw) != "" {
		if !strings.HasPrefix(strings.TrimSpace(raw), "-----BEGIN CERTIFICATE-----") {
			return nil, errors.New("unexpected data in PostgreSQL CA bundle")
		}
		block, rest := pem.Decode([]byte(raw))
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("invalid PostgreSQL CA bundle")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return nil, errors.New("PostgreSQL trust bundle contains a non-CA certificate")
		}
		pool.AddCert(certificate)
		count++
		raw = string(rest)
	}
	if count == 0 {
		return nil, errors.New("empty PostgreSQL CA bundle")
	}
	return pool, nil
}

func managedConnectionConfig(value, expectedRole, rootCA string) (*pgx.ConnConfig, error) {
	parsed, err := managedPostgresURL(value, expectedRole)
	if err != nil {
		return nil, err
	}
	roots, err := postgresRoots(rootCA)
	if err != nil {
		return nil, err
	}
	config, err := pgx.ParseConfig(value)
	if err != nil {
		return nil, errors.New("invalid managed runtime connection configuration")
	}
	// ParseConfig may read libpq environment defaults. Replace every connection
	// authority and session option used here; no plaintext/address fallback,
	// client key, ambient root CA, pgpass or attacker-controlled SQL options.
	config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: parsed.Hostname()}
	config.Fallbacks = nil
	config.ConnectTimeout = 10 * time.Second
	config.RuntimeParams = map[string]string{"application_name": "leapview-managed-recovery", "default_transaction_read_only": "on", "statement_timeout": "15000"}
	config.DialFunc = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	return config, nil
}
