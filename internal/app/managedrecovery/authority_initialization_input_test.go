package managedrecovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedAuthorityInitializationInputRejectsUnsafeIdentityAndFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	input := ManagedAuthorityInitializationInput{SchemaVersion: 1, OriginalSystemIdentifier: "source", OwnerRole: "leapview_recovery_owner", ReceiptFile: filepath.Join(root, "receipt"), Bootstrap: AuthorityInput{URLFile: filepath.Join(root, "admin"), RootCAFile: filepath.Join(root, "ca"), Role: "postgres", SystemIdentifier: "independent"}, Operator: AuthorityInput{URLFile: filepath.Join(root, "operator"), RootCAFile: filepath.Join(root, "ca"), Role: "leapview_recovery_operator", SystemIdentifier: "independent"}}
	path := filepath.Join(root, "input")
	value, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, value, 0600))
	_, err = ReadManagedAuthorityInitializationInput(path)
	require.NoError(t, err)
	for _, raw := range [][]byte{append(append([]byte{}, value...), []byte(` {}`)...), []byte(`{"schemaVersion":1,"schemaVersion":1}`), []byte(`{"schemaVersion":1,"arbitraryAuthority":true}`)} {
		require.NoError(t, os.WriteFile(path, raw, 0600))
		_, err := ReadManagedAuthorityInitializationInput(path)
		require.Error(t, err)
	}
	for name, modify := range map[string]func(*ManagedAuthorityInitializationInput){
		"source": func(i *ManagedAuthorityInitializationInput) {
			i.OriginalSystemIdentifier = i.Bootstrap.SystemIdentifier
		},
		"operator system":      func(i *ManagedAuthorityInitializationInput) { i.Operator.SystemIdentifier = "foreign" },
		"owner":                func(i *ManagedAuthorityInitializationInput) { i.OwnerRole = "leapview_control_owner" },
		"runtime operator":     func(i *ManagedAuthorityInitializationInput) { i.Operator.Role = "leapview_control_runtime" },
		"schema":               func(i *ManagedAuthorityInitializationInput) { i.SchemaVersion = 2 },
		"credential overwrite": func(i *ManagedAuthorityInitializationInput) { i.ReceiptFile = i.Bootstrap.URLFile },
	} {
		t.Run(name, func(t *testing.T) {
			bad := input
			modify(&bad)
			_, err := bad.Initialize(t.Context())
			require.ErrorContains(t, err, "authority")
		})
	}
	input.ReceiptFile = path
	value, err = json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, value, 0600))
	_, err = ReadManagedAuthorityInitializationInput(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = ReadManagedAuthorityInitializationInput(path)
	require.Error(t, err)
}
