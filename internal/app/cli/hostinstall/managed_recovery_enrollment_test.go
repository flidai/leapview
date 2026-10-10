package hostinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/stretchr/testify/require"
)

func TestManagedEnrollmentLocksSourceHomeBeforeOpeningAuthorities(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.Chmod(home, 0700))
	input := managedrecovery.ManagedEnrollmentInput{SchemaVersion: 1, Request: managedrecovery.ManagedEnrollmentRequest{InstanceHome: home}, ReceiptFile: filepath.Join(home, "receipt.json"), Source: managedrecovery.AuthorityInput{URLFile: filepath.Join(home, "must-not-be-read")}}
	value, err := json.Marshal(input)
	require.NoError(t, err)
	path := filepath.Join(home, "enrollment.json")
	require.NoError(t, os.WriteFile(path, value, 0600))
	lock, err := instancelock.Acquire(home)
	require.NoError(t, err)
	defer lock.Release()
	command := Command(t.Context(), CommandOptions{})
	command.SetArgs([]string{"enroll-managed-recovery", "--input", path})
	require.ErrorContains(t, command.Execute(), "exclusive source instance ownership")
	_, err = os.Lstat(input.ReceiptFile)
	require.True(t, os.IsNotExist(err), "locked instance must not publish enrollment receipt")
}
