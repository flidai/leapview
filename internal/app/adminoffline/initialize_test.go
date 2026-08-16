package adminoffline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestAdminInitializeCreatesOneTimeCredentialBundle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_PRODUCTION", "1")
	t.Setenv("LEAPVIEW_ENVIRONMENT", "prod")
	t.Setenv("LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL", "owner@example.com")
	var out bytes.Buffer
	if err := runAdminInitialize(context.Background(), "json", &out); err != nil {
		t.Fatal(err)
	}
	var credentials initialInstanceCredentials
	if err := json.Unmarshal(out.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials.Email != "owner@example.com" || credentials.TemporaryPassword == "" || credentials.PublisherToken == "" || credentials.PublisherTokenExpiresAt == "" {
		t.Fatalf("credentials = %#v", credentials)
	}
	expires, err := time.Parse(time.RFC3339, credentials.PublisherTokenExpiresAt)
	if err != nil || time.Until(expires) > 24*time.Hour || time.Until(expires) < 23*time.Hour {
		t.Fatalf("publisher expiry = %q, %v", credentials.PublisherTokenExpiresAt, err)
	}
	store, err := platform.Open(context.Background(), filepath.Join(home, "leapview.db"))
	require.NoError(t, err)
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, local, err := repo.VerifyLocalPassword(context.Background(), credentials.Email, credentials.TemporaryPassword)
	if err != nil || !local.MustChangePassword {
		t.Fatalf("initialized administrator = %#v credential=%#v err=%v", principal, local, err)
	}
	apiCredential, err := repo.CredentialForAPIToken(context.Background(), credentials.PublisherToken)
	if err != nil {
		t.Fatalf("publisher credential = %#v err=%v", apiCredential, err)
	}
	require.Equal(t, access.InitialPublisherCapabilities(), apiCredential.Token.Capabilities)
	var role string
	require.NoError(t, store.SQLDB().QueryRowContext(
		context.Background(),
		`SELECT role FROM platform_role_bindings WHERE principal_id = ?`,
		principal.ID,
	).Scan(&role))
	require.Equal(t, string(access.PlatformRoleAdmin), role)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := acknowledgeInitialCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runAdminInitialize(context.Background(), "json", &out); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("second initialize error = %v", err)
	}
}

func TestAdminInitializeEvaluationPublisherCanStageDataWithoutAdminAuthority(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_PRODUCTION", "1")
	t.Setenv("LEAPVIEW_ENVIRONMENT", "evaluation")
	t.Setenv("LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL", "admin@localhost")
	var out bytes.Buffer
	if err := runAdminInitialize(
		context.Background(),
		"json",
		&out,
	); err != nil {
		t.Fatal(err)
	}
	var credentials initialInstanceCredentials
	if err := json.Unmarshal(out.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	store, err := platform.Open(
		context.Background(),
		filepath.Join(home, "leapview.db"),
	)
	require.NoError(t, err)
	defer store.Close()
	credential, err := accesssqlite.NewRepository(
		store.SQLDB(),
	).CredentialForAPIToken(
		context.Background(),
		credentials.PublisherToken,
	)
	require.NoError(t, err)
	require.Equal(t, access.LocalEvaluationPublisherCapabilities(), credential.Token.Capabilities)
	require.NotContains(t, credential.Token.Capabilities, access.CapabilityProjectAdmin)
}

func TestAdminInitializeReplaysCredentialsAfterDeliveryFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_PRODUCTION", "1")
	t.Setenv("LEAPVIEW_ENVIRONMENT", "prod")
	t.Setenv("LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL", "owner@example.com")

	if err := runAdminInitialize(context.Background(), "json", errorWriter{}); err == nil {
		t.Fatal("initialize output failure = nil")
	}
	var recovered bytes.Buffer
	if err := runAdminInitialize(context.Background(), "json", &recovered); err != nil {
		t.Fatalf("recover initialization credentials: %v", err)
	}
	var credentials initialInstanceCredentials
	if err := json.Unmarshal(recovered.Bytes(), &credentials); err != nil || credentials.TemporaryPassword == "" || credentials.PublisherToken == "" {
		t.Fatalf("recovered credentials = %#v, %v", credentials, err)
	}
	if err := acknowledgeInitialCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(initialCredentialRecoveryPath(home)); !os.IsNotExist(err) {
		t.Fatalf("credential recovery file remains after acknowledgement: %v", err)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("credential destination failed")
}
