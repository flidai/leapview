package cliapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	instancelock "github.com/flidai/leapview/internal/platform/locking"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func validateTestProjectResourceID(value string) error {
	_, err := projectgraph.NewResourceID(value)
	return err
}

func TestProfileStoreProjectAuthorityMintsOnceAndSurvivesTargetRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	store := NewProfileStore(path)
	first, err := store.ResolveProjectAuthority("", validateTestProjectResourceID)
	require.NoError(t, err)
	second, err := NewProfileStore(path).ResolveProjectAuthority("", validateTestProjectResourceID)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.NotEmpty(t, first.IssuerID)
	require.NotEmpty(t, first.ProjectUID)

	profile := TargetProfile{Origin: "https://example.com", InstanceID: "lvinst_one", Environment: "development", CredentialAccount: "one", ProjectID: first.ProjectUID}
	require.NoError(t, store.Put("local", profile))
	require.NoError(t, store.Delete("local"))
	afterDelete, err := store.ResolveProjectAuthority("", validateTestProjectResourceID)
	require.NoError(t, err)
	require.Equal(t, first, afterDelete)

	profile.InstanceID = "lvinst_two"
	profile.Environment = "production"
	require.NoError(t, store.Put("recreated", profile))
	require.Equal(t, first.ProjectUID, profile.ProjectID)
}

func TestProfileStoreProjectAuthorityAcceptsExternalUIDAndRejectsReplacement(t *testing.T) {
	store := NewProfileStore(filepath.Join(t.TempDir(), "cli.json"))
	authority, err := store.ResolveProjectAuthority("project:externally-issued", validateTestProjectResourceID)
	require.NoError(t, err)
	require.Equal(t, "project:externally-issued", authority.ProjectUID)

	replay, err := store.ResolveProjectAuthority("project:externally-issued", validateTestProjectResourceID)
	require.NoError(t, err)
	require.Equal(t, authority, replay)

	_, err = store.ResolveProjectAuthority("project:forged-replacement", validateTestProjectResourceID)
	require.ErrorIs(t, err, ErrProjectAuthorityConflict)
}

func TestProfileStoreProjectAuthorityCanBindSameUIDToSeparateTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	store := NewProfileStore(path)
	authority, err := store.ResolveProjectAuthority("project:shared", validateTestProjectResourceID)
	require.NoError(t, err)
	for name, profile := range map[string]TargetProfile{
		"development": {Origin: "http://127.0.0.1:8080", InstanceID: "lvinst_dev", Environment: "development", CredentialAccount: "dev", ProjectID: authority.ProjectUID},
		"production":  {Origin: "https://prod.example.com", InstanceID: "lvinst_prod", Environment: "production", CredentialAccount: "prod", ProjectID: authority.ProjectUID},
	} {
		require.NoError(t, store.Put(name, profile))
	}
	dev, err := store.Get("development")
	require.NoError(t, err)
	prod, err := store.Get("production")
	require.NoError(t, err)
	require.Equal(t, dev.ProjectID, prod.ProjectID)
}

func TestProfileStoreProjectAuthorityConcurrentFirstUseConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	const callers = 8
	results := make(chan ProjectAuthority, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			authority, err := NewProfileStore(path).ResolveProjectAuthority("", validateTestProjectResourceID)
			results <- authority
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var expected ProjectAuthority
	for authority := range results {
		if expected.ProjectUID == "" {
			expected = authority
		}
		require.Equal(t, expected, authority)
	}
}

func TestProfileStorePersistsOnlyNonSecretTargetMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	store := NewProfileStore(path)
	profile := TargetProfile{
		Origin:            "https://analytics.example.com/",
		InstanceID:        "lvinst_01",
		Environment:       "production",
		CredentialAccount: "target/lvinst_01/project",
		ProjectID:         "project",
	}
	if err := store.Put("prod", profile); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("prod")
	require.NoError(t, err)
	if got.Origin != "https://analytics.example.com" || got.InstanceID != profile.InstanceID || got.ProjectID != profile.ProjectID {
		t.Fatalf("profile = %+v", got)
	}
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, forbidden := range []string{"accessToken", "refreshToken", "password", "secret"} {
		if strings.Contains(strings.ToLower(string(content)), strings.ToLower(forbidden)) {
			t.Fatalf("profile contains secret-bearing field %q: %s", forbidden, content)
		}
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %o, want 600", info.Mode().Perm())
	}
}

func TestProfileStoreRefusesReadModifyWriteWhileAnotherProcessOwnsLock(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "cli.json")
	store := NewProfileStore(path)
	if err := store.Put("existing", TargetProfile{
		Origin: "https://existing.example", InstanceID: "lvinst_existing",
		CredentialAccount: "existing", ProjectID: "project",
	}); err != nil {
		t.Fatal(err)
	}
	lock, err := instancelock.AcquireNamed(directory, ".cli.json.lock")
	require.NoError(t, err)
	defer lock.Release()

	err = store.Put("concurrent", TargetProfile{
		Origin: "https://concurrent.example", InstanceID: "lvinst_concurrent",
		CredentialAccount: "concurrent", ProjectID: "project",
	})
	if err == nil {
		t.Fatal("Put succeeded while another process owned the profile lock")
	}
	if _, getErr := store.Get("existing"); getErr != nil {
		t.Fatalf("existing profile was corrupted: %v", getErr)
	}
	if _, getErr := store.Get("concurrent"); !errors.Is(getErr, ErrProfileNotFound) {
		t.Fatalf("concurrent profile unexpectedly persisted: %v", getErr)
	}
}

func TestProfileStoreRejectsLegacyPlaintextCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	content := `{"version":1,"targets":{"prod":{"origin":"https://example.test","instanceId":"lvinst_01","token":"plaintext"}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProfileStore(path).Get("prod"); err == nil || !strings.Contains(err.Error(), "secret-bearing") {
		t.Fatalf("Get error = %v, want secret-bearing field rejection", err)
	}
}

func TestProfileStoreRejectsMutableOrUnsafeTargetIdentity(t *testing.T) {
	store := NewProfileStore(filepath.Join(t.TempDir(), "cli.json"))
	tests := []TargetProfile{
		{Origin: "http://analytics.example.com", InstanceID: "lvinst_01", CredentialAccount: "account", ProjectID: "project"},
		{Origin: "https://user:password@example.com", InstanceID: "lvinst_01", CredentialAccount: "account", ProjectID: "project"},
		{Origin: "https://example.com/path", InstanceID: "lvinst_01", CredentialAccount: "account", ProjectID: "project"},
		{Origin: "https://example.com", InstanceID: "", CredentialAccount: "account", ProjectID: "project"},
	}
	for index, profile := range tests {
		if err := store.Put("prod", profile); err == nil {
			t.Errorf("case %d accepted unsafe profile %+v", index, profile)
		}
	}
	if err := store.Put("local", TargetProfile{
		Origin:            "http://127.0.0.1:8080",
		InstanceID:        "lvinst_local",
		Environment:       "development",
		CredentialAccount: "account",
		ProjectID:         "project",
	}); err != nil {
		t.Fatalf("loopback development target rejected: %v", err)
	}
}

func TestProfileStoreRefusesInstanceIdentityReplacement(t *testing.T) {
	store := NewProfileStore(filepath.Join(t.TempDir(), "cli.json"))
	first := TargetProfile{
		Origin:            "https://example.com",
		InstanceID:        "lvinst_first",
		CredentialAccount: "account-first",
		ProjectID:         "project",
	}
	if err := store.Put("prod", first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.InstanceID = "lvinst_second"
	second.CredentialAccount = "account-second"
	if err := store.Put("prod", second); err == nil || !strings.Contains(err.Error(), "instance identity") {
		t.Fatalf("Put error = %v, want immutable instance identity rejection", err)
	}
}

func TestProfileStoreFindsStableNameByCanonicalOrigin(t *testing.T) {
	store := NewProfileStore(filepath.Join(t.TempDir(), "cli.json"))
	if err := store.Put("production", TargetProfile{
		Origin: "https://example.com", InstanceID: "lvinst_prod", CredentialAccount: "account", ProjectID: "project",
	}); err != nil {
		t.Fatal(err)
	}
	name, profile, err := store.FindByOrigin("https://example.com/")
	require.NoError(t, err)
	if name != "production" || profile.InstanceID != "lvinst_prod" {
		t.Fatalf("name=%q profile=%+v", name, profile)
	}
}
