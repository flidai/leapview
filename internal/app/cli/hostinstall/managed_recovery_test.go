package hostinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake/metadata"
	"github.com/flidai/leapview/internal/app/managedrecovery"
	"github.com/flidai/leapview/internal/app/providerrestore"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

func TestManagedRecoveryLocksActualHomeWithExternalKeyringBeforeAuthorityEffects(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("actual production command requires unprivileged recovery owner")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	secretRoot := t.TempDir()
	if err := os.Chmod(secretRoot, 0700); err != nil {
		t.Fatal(err)
	}
	credentials := managedrecovery.ManagedCredentials{KeyringPath: filepath.Join(secretRoot, "external-keyring.json")}
	credentialsPath := filepath.Join(secretRoot, "credentials.json")
	value, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, value, 0600); err != nil {
		t.Fatal(err)
	}
	closure, err := metadata.NewNativeSnapshotClosureEvidence("catalog", 1, filepath.Join(home, "data"), "_candidate", []metadata.BaseTable{{Schema: "_candidate", Table: "orders"}}, []metadata.NativeSnapshotObject{})
	if err != nil {
		t.Fatal(err)
	}
	input := managedrecovery.ManagedInput{SchemaVersion: 1, Profile: providerrestore.ManagedLocalProfile, InstanceHome: home, RecoverySetID: "set", OccurrenceID: "occurrence", CredentialsFile: credentialsPath, Closure: closure, ValidationAttemptID: "validation", Validator: "operator", Publisher: "publisher", Authority: managedrecovery.AuthorityInput{URLFile: filepath.Join(secretRoot, "must-not-be-read")}}
	value, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(secretRoot, "input.json")
	if err := os.WriteFile(inputPath, value, 0600); err != nil {
		t.Fatal(err)
	}
	servingLock, err := instancelock.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer servingLock.Release()
	command := Command(t.Context(), CommandOptions{})
	command.SetArgs([]string{"restore-managed", "--input", inputPath})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "exclusive instance ownership") {
		t.Fatalf("recovery bypassed actual serving home lock: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(secretRoot, instancelock.FileName)); !os.IsNotExist(err) {
		t.Fatal("recovery locked the unrelated secret directory")
	}
}
