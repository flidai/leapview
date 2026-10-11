package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func managedJourneyInitializeAuthority(t *testing.T, harness *postgrestest.Harness, database *postgrestest.Database, sourceSystemID string) (string, string) {
	t.Helper()
	connection, err := pgx.Connect(t.Context(), productionAdmissionTLSURL(database.AdminURL(), harness.RootCertPath()))
	require.NoError(t, err)
	defer connection.Close(t.Context())
	var systemID string
	require.NoError(t, connection.QueryRow(t.Context(), "SELECT system_identifier::text FROM pg_control_system()").Scan(&systemID))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	operator := postgrestest.Role{Name: "leapview_recovery_operator_" + suffix, Password: "private journey operator password", Login: true}
	operatorURL := database.PrivateURL(operator)
	enrolledEndpoint, err := url.Parse(operatorURL)
	require.NoError(t, err)
	privateConnection := func(name, rawURL string) managedrecovery.AuthorityInput {
		t.Helper()
		parsed, err := url.Parse(rawURL)
		require.NoError(t, err)
		parsed.Scheme, parsed.RawQuery = "postgres", "sslmode=verify-full"
		parsed.Host = enrolledEndpoint.Host
		ca, err := os.ReadFile(harness.RootCertPath())
		require.NoError(t, err)
		input := managedrecovery.AuthorityInput{URLFile: filepath.Join(root, name+".url"), RootCAFile: filepath.Join(root, name+".ca"), Role: parsed.User.Username(), SystemIdentifier: systemID}
		require.NoError(t, os.WriteFile(input.URLFile, []byte(parsed.String()), 0600))
		require.NoError(t, os.WriteFile(input.RootCAFile, ca, 0600))
		return input
	}
	input := managedrecovery.ManagedAuthorityInitializationInput{SchemaVersion: 1, OriginalSystemIdentifier: sourceSystemID, OwnerRole: "leapview_recovery_owner_" + suffix, Bootstrap: privateConnection("bootstrap", database.AdminURL()), Operator: privateConnection("operator", operatorURL), ReceiptFile: filepath.Join(root, "authority-receipt.json")}
	value, err := json.Marshal(input)
	require.NoError(t, err)
	path := filepath.Join(root, "init.json")
	require.NoError(t, os.WriteFile(path, value, 0600))
	input, err = managedrecovery.ReadManagedAuthorityInitializationInput(path)
	require.NoError(t, err)
	receipt, err := input.Initialize(t.Context())
	require.NoError(t, err)
	require.Equal(t, "initialized", receipt.Status)
	replay, err := input.Initialize(t.Context())
	require.NoError(t, err)
	require.Equal(t, receipt, replay)
	return operatorURL, systemID
}
