package encryption

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type budgetStub struct {
	calls        int
	deploymentID string
	keyID        string
	commitment   KeyCommitment
	err          error
}

func (b *budgetStub) ReserveEncryption(_ context.Context, deploymentID, keyID string, commitment KeyCommitment) error {
	b.calls++
	b.deploymentID = deploymentID
	b.keyID = keyID
	b.commitment = commitment
	return b.err
}

func TestEncryptDecryptBindsContextAndAuthenticatesCiphertext(t *testing.T) {
	keyring := loadTestKeyring(t, testKeyringJSON(t, "deployment-a", "write-a", []testKey{{id: "write-a", key: bytes32(1), state: "active_write"}}), 0o600)
	binding := testBinding()
	budget := &budgetStub{}
	plaintext := []byte("{\"password\":\"do-not-trim \"}\n ")

	envelope, err := keyring.Encrypt(context.Background(), budget, binding, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	wantCommitment, ok := keyring.KeyCommitment("write-a")
	if !ok || budget.calls != 1 || budget.deploymentID != binding.DeploymentID || budget.keyID != "write-a" || budget.commitment != wantCommitment {
		t.Fatalf("reservation = %+v", budget)
	}
	if envelope.Format != envelopeFormat || envelope.KeyID != "write-a" {
		t.Fatalf("envelope identity = %q/%q", envelope.Format, envelope.KeyID)
	}
	if len(envelope.Ciphertext) < 28 {
		t.Fatalf("ciphertext length = %d, want nonce and tag", len(envelope.Ciphertext))
	}
	got, err := keyring.Decrypt(binding, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("plaintext = %q, want byte-for-byte input", got)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(envelope.Ciphertext)) || strings.Contains(string(encoded), "Ciphertext") {
		t.Fatalf("generic JSON serialization exposed ciphertext: %s", encoded)
	}

	wrongBinding := binding
	wrongBinding.Destination = "warehouse-b"
	if _, err := keyring.Decrypt(wrongBinding, envelope); err == nil {
		t.Fatal("decrypt accepted a different destination")
	}
	if _, err := keyring.Decrypt(testBindingWithDeployment("deployment-b"), envelope); err == nil {
		t.Fatal("decrypt accepted a different deployment")
	}
	if _, err := keyring.Decrypt(binding, Envelope{Format: "unknown", KeyID: envelope.KeyID, Ciphertext: envelope.Ciphertext}); err == nil {
		t.Fatal("decrypt accepted an unknown envelope format")
	}

	tampered := Envelope{Format: envelope.Format, KeyID: envelope.KeyID, Ciphertext: append([]byte(nil), envelope.Ciphertext...)}
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 0x80
	if _, err := keyring.Decrypt(binding, tampered); err == nil {
		t.Fatal("decrypt accepted tampered ciphertext")
	}
}

func TestEncryptUsesFreshRandomNonceAndEnforcesPlaintextLimit(t *testing.T) {
	keyring := loadTestKeyring(t, testKeyringJSON(t, "deployment-a", "write-a", []testKey{{id: "write-a", key: bytes32(2), state: "active_write"}}), 0o600)
	budget := &budgetStub{}
	binding := testBinding()
	plaintext := []byte("same payload")
	one, err := keyring.Encrypt(context.Background(), budget, binding, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	two, err := keyring.Encrypt(context.Background(), budget, binding, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if string(one.Ciphertext) == string(two.Ciphertext) {
		t.Fatal("same plaintext produced repeated nonce-prefixed ciphertext")
	}
	if budget.calls != 2 {
		t.Fatalf("reservation calls = %d, want 2", budget.calls)
	}
	if _, err := keyring.Encrypt(context.Background(), budget, binding, make([]byte, MaxPlaintextSize+1)); err == nil {
		t.Fatal("oversized plaintext was encrypted")
	}
	if budget.calls != 2 {
		t.Fatalf("oversized plaintext reserved budget: calls = %d", budget.calls)
	}
}

func TestEncryptRequiresBudgetAndDoesNotEncryptWhenReservationFails(t *testing.T) {
	keyring := loadTestKeyring(t, testKeyringJSON(t, "deployment-a", "write-a", []testKey{{id: "write-a", key: bytes32(3), state: "active_write"}}), 0o600)
	if _, err := keyring.Encrypt(context.Background(), nil, testBinding(), []byte("secret")); err == nil {
		t.Fatal("encrypt accepted a nil budget")
	}
	budget := &budgetStub{err: fmt.Errorf("budget exhausted")}
	envelope, err := keyring.Encrypt(context.Background(), budget, testBinding(), []byte("secret"))
	if err == nil {
		t.Fatal("encrypt succeeded after reservation failure")
	}
	if budget.calls != 1 || len(envelope.Ciphertext) != 0 {
		t.Fatalf("reservation failure result = calls %d, ciphertext length %d", budget.calls, len(envelope.Ciphertext))
	}
	budget = &budgetStub{err: fmt.Errorf("wrapped: %w", ErrBudgetExhausted)}
	if _, err := keyring.Encrypt(context.Background(), budget, testBinding(), []byte("secret")); err != ErrBudgetExhausted {
		t.Fatalf("exhausted budget error = %v", err)
	}
}

func TestLoadRequiresStrictKeyringAndDoesNotEchoInput(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secretKey := base64.StdEncoding.EncodeToString(bytes32(9))
	valid := `{"format":"credential-keyring-v1","deployment_id":"deployment-a","active_write_key_id":"write-a","keys":[{"key_id":"write-a","key_base64":"` + secretKey + `","state":"active_write"}]}`
	malformed := []string{
		strings.Replace(valid, `"format":"credential-keyring-v1"`, `"format":"credential-keyring-v1","format":"credential-keyring-v1"`, 1),
		strings.Replace(valid, `"format":"credential-keyring-v1"`, `"format":"credential-keyring-v1","Format":"credential-keyring-v1"`, 1),
		strings.Replace(valid, `"state":"active_write"`, `"state":"decrypt_only"`, 1),
		strings.Replace(valid, `"key_base64":"`+secretKey+`"`, `"key_base64":"not-base64-secret"`, 1),
		strings.Replace(valid, `"key_base64":"`+secretKey+`"`, `"key_base64":"`+strings.Repeat("A", 44)+`"`, 1),
		strings.Replace(valid, `"keys":[`, `"unexpected":"secret-input","keys":[`, 1),
	}
	for i, raw := range malformed {
		path := filepath.Join(root, fmt.Sprintf("keyring-%d.json", i))
		writeKeyring(t, path, []byte(raw), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("malformed keyring %d was accepted", i)
		} else if strings.Contains(err.Error(), "not-base64-secret") || strings.Contains(err.Error(), "secret-input") || strings.Contains(err.Error(), secretKey) {
			t.Errorf("error %d disclosed keyring input: %v", i, err)
		}
	}

	duplicateKeyID := testKeyringJSON(t, "deployment-a", "write-a", []testKey{
		{id: "write-a", key: bytes32(1), state: "active_write"},
		{id: "write-a", key: bytes32(2), state: "decrypt_only"},
	})
	path := filepath.Join(root, "duplicate-id.json")
	writeKeyring(t, path, []byte(duplicateKeyID), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("duplicate key ID was accepted")
	}
	duplicateMaterial := testKeyringJSON(t, "deployment-a", "write-a", []testKey{
		{id: "write-a", key: bytes32(1), state: "active_write"},
		{id: "old-a", key: bytes32(1), state: "decrypt_only"},
	})
	path = filepath.Join(root, "duplicate-material.json")
	writeKeyring(t, path, []byte(duplicateMaterial), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("same key material under multiple IDs was accepted")
	}
}

func TestLoadChecksExactPrivatePermissionsOwnerAndSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte(testKeyringJSON(t, "deployment-a", "write-a", []testKey{{id: "write-a", key: bytes32(1), state: "active_write"}}))
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := filepath.Join(root, fmt.Sprintf("ring-%04o.json", mode))
		writeKeyring(t, path, contents, mode)
		if _, err := Load(path); err != nil {
			t.Errorf("mode %04o rejected: %v", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o640, 0o644, 0o000} {
		path := filepath.Join(root, fmt.Sprintf("ring-%04o.json", mode))
		writeKeyring(t, path, contents, mode)
		if _, err := Load(path); err == nil {
			t.Errorf("mode %04o accepted", mode)
		}
	}
	setuidPath := filepath.Join(root, "setuid-ring.json")
	writeKeyring(t, setuidPath, contents, os.ModeSetuid|0o600)
	setuidInfo, err := os.Stat(setuidPath)
	if err != nil {
		t.Fatal(err)
	}
	if setuidInfo.Mode()&os.ModeSetuid != 0 {
		if _, err := Load(setuidPath); err == nil {
			t.Fatal("keyring with a special permission bit was accepted")
		}
	}
	path := filepath.Join(root, "real.json")
	writeKeyring(t, path, contents, 0o600)
	link := filepath.Join(filepath.Dir(path), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink keyring was accepted")
	}
	symlinkDirectory := filepath.Join(root, "linked-directory")
	if err := os.Symlink(filepath.Dir(path), symlinkDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(symlinkDirectory, filepath.Base(path))); err == nil {
		t.Fatal("keyring under a symlink directory was accepted")
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatalf("prepare owner mismatch: %v", err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("keyring owned by another user was accepted")
		}
	}
}

func TestDecryptRejectsMissingOrWrongKeyAndBindingShape(t *testing.T) {
	keyring := loadTestKeyring(t, testKeyringJSON(t, "deployment-a", "write-a", []testKey{
		{id: "write-a", key: bytes32(4), state: "active_write"},
		{id: "old-a", key: bytes32(5), state: "decrypt_only"},
	}), 0o600)
	if _, err := keyring.Decrypt(testBinding(), Envelope{Format: envelopeFormat, KeyID: "missing", Ciphertext: make([]byte, 28)}); err == nil {
		t.Fatal("decrypt accepted a missing key")
	}
	envelope, err := keyring.Encrypt(context.Background(), &budgetStub{}, testBinding(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	envelope.KeyID = "old-a"
	if _, err := keyring.Decrypt(testBinding(), envelope); err == nil {
		t.Fatal("decrypt accepted ciphertext under the wrong known key")
	}
	invalid := testBinding()
	invalid.ProjectID = ""
	if _, err := keyring.Encrypt(context.Background(), &budgetStub{}, invalid, []byte("secret")); err == nil {
		t.Fatal("connection binding with a missing project was accepted")
	}
	invalid = testBinding()
	invalid.ScopeKind = "agent"
	if _, err := keyring.Encrypt(context.Background(), &budgetStub{}, invalid, []byte("secret")); err == nil {
		t.Fatal("agent binding with connection scope fields was accepted")
	}
	invalid = testBinding()
	invalid.OwnerID = strings.Repeat("o", 256)
	if err := invalid.Validate(); err == nil {
		t.Fatal("oversized binding field was accepted")
	}
	invalid.OwnerID = string([]byte{0xff})
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid UTF-8 binding field was accepted")
	}
}

type testKey struct {
	id    string
	key   []byte
	state string
}

func testKeyringJSON(t *testing.T, deploymentID, active string, keys []testKey) string {
	t.Helper()
	var encoded []string
	for _, key := range keys {
		encoded = append(encoded, fmt.Sprintf(`{"key_id":%q,"key_base64":%q,"state":%q}`, key.id, base64.StdEncoding.EncodeToString(key.key), key.state))
	}
	return fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":%q,"keys":[%s]}`, deploymentID, active, strings.Join(encoded, ","))
}

func bytes32(fill byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill
	}
	return key
}

func testBinding() Binding {
	return Binding{
		DeploymentID: "deployment-a",
		OwnerID:      "owner-a",
		ScopeKind:    "connection",
		TargetID:     "target-a",
		ProjectID:    "project-a",
		Environment:  "production",
		ResourceID:   "connection-a",
		Purpose:      "warehouse-login",
		Provider:     "postgres",
		Destination:  "warehouse-a",
		VersionID:    "version-a",
	}
}

func testBindingWithDeployment(deploymentID string) Binding {
	binding := testBinding()
	binding.DeploymentID = deploymentID
	return binding
}

func loadTestKeyring(t *testing.T, contents string, mode os.FileMode) *Keyring {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "keyring.json")
	writeKeyring(t, path, []byte(contents), mode)
	keyring, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return keyring
}

func writeKeyring(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
