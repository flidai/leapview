package managedrecovery

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
)

type managedCredentialOwner struct{ owner string }

func (owner managedCredentialOwner) CustomerOwner(context.Context) (string, error) {
	return owner.owner, nil
}

func TestManagedKeyringRequiresRetainedBytesAndDurableInstanceOwner(t *testing.T) {
	_, credentials, _ := managedCredentialFixture(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	credentials.KeyringPath = filepath.Join(root, "keyring.json")
	credentials.InstanceID = "lvinst_0123456789abcdef0123456789abcdef"
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	raw, err := json.Marshal(map[string]any{"format": "credential-keyring-v1", "deployment_id": credentials.InstanceID, "active_write_key_id": "key-1", "keys": []map[string]any{{"key_id": "key-1", "key_base64": key, "state": "active_write"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials.KeyringPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	credentials.KeyringDigest = digestBytes(raw)
	if err := VerifyManagedKeyring(t.Context(), credentials, managedCredentialOwner{owner: "customer:one"}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManagedKeyring(t.Context(), credentials, managedCredentialOwner{}); err == nil {
		t.Fatal("keyring file replaced missing durable customer owner")
	}
	wrong := credentials
	wrong.InstanceID = "lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := VerifyManagedKeyring(t.Context(), wrong, managedCredentialOwner{owner: "customer:one"}); err == nil {
		t.Fatal("foreign durable instance accepted")
	}
	if err := os.WriteFile(credentials.KeyringPath, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManagedKeyring(t.Context(), credentials, managedCredentialOwner{owner: "customer:one"}); err == nil {
		t.Fatal("different valid keyring bytes accepted")
	}
}

func managedCredentialFixture(t *testing.T) (providerrestore.ReplacementHandoff, ManagedCredentials, RuntimeRoles) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "managed test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	credentials := ManagedCredentials{SchemaVersion: 1, Profile: providerrestore.ManagedLocalProfile, RecoverySetID: "set", TargetID: "target", OccurrenceID: "occurrence", ControlURL: "postgres://control_runtime:control-secret@postgres.leapview.dev:5544/control?sslmode=verify-full", DuckLakeURL: "postgres://duck_runtime:duck-secret@postgres.leapview.dev:5544/ducklake?sslmode=verify-full", PostgresRootCA: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), InstanceID: "instance", KeyringPath: "/var/lib/leapview/credential-keyring.json", KeyringDigest: "sha256:" + strings.Repeat("a", 64)}
	handoff := providerrestore.ReplacementHandoff{SchemaVersion: providerrestore.ManagedLocalHandoffSchemaVersion, RecoverySetID: credentials.RecoverySetID, TargetID: credentials.TargetID, ManagedLocal: &providerrestore.ManagedLocalHandoff{Profile: credentials.Profile, OccurrenceID: credentials.OccurrenceID}, Providers: []providerrestore.ProviderEndpoint{
		{Role: "control", Endpoint: "postgres://postgres.leapview.dev:5544?sslmode=verify-full", Database: "control", CredentialSecretKey: "postgres.control.url", TLSRootCASecretKey: "postgres.root-ca"},
		{Role: "ducklake", Endpoint: "postgres://postgres.leapview.dev:5544?sslmode=verify-full", Database: "ducklake", CredentialSecretKey: "postgres.ducklake.url", TLSRootCASecretKey: "postgres.root-ca"},
	}}
	return handoff, credentials, RuntimeRoles{Control: "control_runtime", DuckLake: "duck_runtime"}
}

func TestManagedCredentialsBindExactOccurrenceEndpointsRolesAndPrivateKeyring(t *testing.T) {
	handoff, credentials, roles := managedCredentialFixture(t)
	if err := credentials.ValidateForHandoff(handoff, roles); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*ManagedCredentials){
		"remote profile":     func(c *ManagedCredentials) { c.Profile = "remote" },
		"remote schema":      func(c *ManagedCredentials) { c.SchemaVersion = 2 },
		"foreign target":     func(c *ManagedCredentials) { c.TargetID = "foreign" },
		"foreign set":        func(c *ManagedCredentials) { c.RecoverySetID = "foreign" },
		"foreign occurrence": func(c *ManagedCredentials) { c.OccurrenceID = "foreign" },
		"wrong role": func(c *ManagedCredentials) {
			c.ControlURL = strings.Replace(c.ControlURL, "control_runtime:", "administrator:", 1)
		},
		"wrong database":   func(c *ManagedCredentials) { c.ControlURL = strings.Replace(c.ControlURL, "/control?", "/another?", 1) },
		"wrong endpoint":   func(c *ManagedCredentials) { c.ControlURL = strings.Replace(c.ControlURL, "5544", "5545", 1) },
		"plaintext":        func(c *ManagedCredentials) { c.ControlURL = strings.Replace(c.ControlURL, "verify-full", "disable", 1) },
		"ambient password": func(c *ManagedCredentials) { c.ControlURL = strings.Replace(c.ControlURL, ":control-secret", "", 1) },
		"SQL options":      func(c *ManagedCredentials) { c.ControlURL += "&options=-csession_authorization=postgres" },
		"ambient CA":       func(c *ManagedCredentials) { c.ControlURL += "&sslrootcert=/ambient/ca" },
		"relative keyring": func(c *ManagedCredentials) { c.KeyringPath = "../keyring" },
		"missing owner":    func(c *ManagedCredentials) { c.InstanceID = "" },
		"unbound keyring":  func(c *ManagedCredentials) { c.KeyringDigest = "" },
		"garbage CA":       func(c *ManagedCredentials) { c.PostgresRootCA = "garbage\n" + c.PostgresRootCA },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			test := credentials
			mutate(&test)
			if err := test.ValidateForHandoff(handoff, roles); err == nil {
				t.Fatal("mismatched managed credential authority accepted")
			}
		})
	}
	remote := handoff
	remote.SchemaVersion = providerrestore.HandoffSchemaVersion
	if err := credentials.ValidateForHandoff(remote, roles); err == nil {
		t.Fatal("managed credentials accepted by remote profile")
	}
}

func TestManagedTLSConfigDiscardsAmbientAuthorityAndPlaintextFallback(t *testing.T) {
	_, credentials, roles := managedCredentialFixture(t)
	t.Setenv("PGAPPNAME", "untrusted")
	t.Setenv("PGOPTIONS", "-c role=postgres")
	t.Setenv("PGPASSWORD", "ambient-password")
	config, err := managedConnectionConfig(credentials.ControlURL, roles.Control, credentials.PostgresRootCA)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Fallbacks) != 0 || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName != "postgres.leapview.dev" || config.Password != "control-secret" || config.RuntimeParams["options"] != "" || config.RuntimeParams["application_name"] != "leapview-managed-recovery" {
		t.Fatal("ambient authority or plaintext TLS fallback retained")
	}
}

func TestManagedPrivateBundleRoundTripRejectsTamperingLinksAndRemoteConsumption(t *testing.T) {
	handoff, credentials, roles := managedCredentialFixture(t)
	store := ManagedSecretStore{Root: t.TempDir()}
	if err := os.Chmod(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	reference, err := store.Save(t.Context(), handoff, roles, credentials)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context(), reference)
	if err != nil || loaded != credentials {
		t.Fatalf("private managed bundle round trip: %v", err)
	}
	if _, err := (providerrestore.FileSecretBundleStore{Root: store.Root}).Load(t.Context(), reference); err == nil {
		t.Fatal("remote v2 consumer accepted managed bundle")
	}
	public, err := json.Marshal(reference)
	if err != nil || strings.Contains(string(public), "control-secret") || strings.Contains(string(public), "duck-secret") || strings.Contains(string(public), credentials.KeyringPath) {
		t.Fatal("private credential material escaped into public reference")
	}
	path := filepath.Join(store.Root, reference.SHA256+".json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), reference); err == nil {
		t.Fatal("world-readable credential file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), reference); err == nil {
		t.Fatal("changed private content accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(store.Root, "other.json")
	raw, _ := json.Marshal(credentials)
	if err := os.WriteFile(other, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), reference); err == nil {
		t.Fatal("credential symlink accepted")
	}
	if _, err := store.Save(t.Context(), handoff, roles, credentials); err == nil {
		t.Fatal("credential symlink accepted during save replay")
	}
}
