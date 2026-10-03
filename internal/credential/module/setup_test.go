package module

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
)

type credentialOwnerReader struct {
	owner string
	err   error
}

func (r credentialOwnerReader) CustomerOwner(context.Context) (string, error) { return r.owner, r.err }

func TestCredentialSetupFailsClosedOnPartialOrMismatchedSetup(t *testing.T) {
	const instance = "lvinst_0123456789abcdefghijklmnopqrstuv"
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "keyring.json")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	body := fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":"key-1","keys":[{"key_id":"key-1","key_base64":%q,"state":"active_write"}]}`, instance, key)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, owner, instance, path string
		readErr                     error
		wantError                   bool
	}{
		{name: "inactive", instance: instance, readErr: platformbootstrap.ErrNotFound},
		{name: "configured", owner: "customer:one", instance: instance, path: path},
		{name: "owner only", owner: "customer:one", instance: instance, wantError: true},
		{name: "keyring only", instance: instance, path: path, readErr: platformbootstrap.ErrNotFound, wantError: true},
		{name: "wrong deployment", owner: "customer:one", instance: "lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", path: path, wantError: true},
		{name: "missing keyring", owner: "customer:one", instance: instance, path: path + "-missing", wantError: true},
		{name: "storage failure", instance: instance, readErr: fmt.Errorf("storage unavailable"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSetup(t.Context(), credentialOwnerReader{tc.owner, tc.readErr}, tc.instance, tc.path)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
			if err != nil && strings.Contains(err.Error(), key) {
				t.Fatal("error exposes key material")
			}
		})
	}
}
