package managedrecovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedEnrollmentInputRejectsAmbiguousAndUnsafeAuthorityFiles(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.Chmod(home, 0700))
	input := ManagedEnrollmentInput{SchemaVersion: 1, Request: ManagedEnrollmentRequest{InstanceHome: home}, ReceiptFile: filepath.Join(home, "receipt.json")}
	value, err := json.Marshal(input)
	require.NoError(t, err)
	path := filepath.Join(home, "input.json")
	require.NoError(t, os.WriteFile(path, value, 0600))
	_, err = ReadManagedEnrollmentInput(path)
	require.NoError(t, err)
	for _, raw := range [][]byte{
		append(append([]byte{}, value...), []byte(` {}`)...),
		[]byte(`{"schemaVersion":1,"schemaVersion":1}`),
		[]byte(`{"schemaVersion":1,"callerSuppliedRecoverySet":{}}`),
	} {
		require.NoError(t, os.WriteFile(path, raw, 0600))
		_, err := ReadManagedEnrollmentInput(path)
		require.Error(t, err)
	}
	input.ReceiptFile = path
	value, err = json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, value, 0600))
	_, err = ReadManagedEnrollmentInput(path)
	require.Error(t, err, "receipt cannot replace private operator input")
	require.NoError(t, os.Chmod(path, 0644))
	_, err = ReadManagedEnrollmentInput(path)
	require.Error(t, err)
}

func TestManagedEnrollmentReceiptNeverOverwritesForeignOrLinkedState(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.Chmod(home, 0700))
	path := filepath.Join(home, "receipt.json")
	receipt := ManagedEnrollmentReceipt{SchemaVersion: 1, OccurrenceID: "exact", Status: "prepared"}
	require.NoError(t, writeManagedEnrollmentReceipt(path, receipt))
	require.NoError(t, writeManagedEnrollmentReceipt(path, receipt))
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	receipt.OccurrenceID = "foreign"
	require.Error(t, writeManagedEnrollmentReceipt(path, receipt))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, actual)
	linked := filepath.Join(home, "linked.json")
	require.NoError(t, os.Symlink(path, linked))
	require.Error(t, writeManagedEnrollmentReceipt(linked, receipt))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
