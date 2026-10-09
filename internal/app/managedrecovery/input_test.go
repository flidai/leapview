package managedrecovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
)

func TestManagedInputRejectsRemoteUnknownAndUntrustedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	_, closure, _ := managedClosureFixture(t)
	input := ManagedInput{InstanceHome: root, SchemaVersion: 1, Profile: providerrestore.ManagedLocalProfile, RecoverySetID: "set", OccurrenceID: "occurrence", ValidationAttemptID: "validation", Validator: "operator", Publisher: "publisher", Closure: closure}
	path := filepath.Join(root, "input.json")
	value, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedInput(path); err != nil {
		t.Fatal(err)
	}
	for name, bytes := range map[string][]byte{
		"remote":                 []byte(`{"schemaVersion":1,"profile":"remote-v2","recoverySetId":"set","occurrenceId":"occurrence","validationAttemptId":"validation","validator":"operator","publisher":"publisher"}`),
		"caller supplied set":    []byte(`{"schemaVersion":1,"profile":"managed-local-v1","recoverySetId":"set","occurrenceId":"occurrence","validationAttemptId":"validation","validator":"operator","publisher":"publisher","recoverySet":{}}`),
		"caller supplied points": []byte(`{"schemaVersion":1,"profile":"managed-local-v1","recoverySetId":"set","occurrenceId":"occurrence","validationAttemptId":"validation","validator":"operator","publisher":"publisher","postgres":{"points":[]}}`),
		"duplicate profile":      []byte(`{"schemaVersion":1,"profile":"managed-local-v1","profile":"remote-v2"}`),
		"trailing":               append(append([]byte{}, value...), []byte(" {}")...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, bytes, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManagedInput(path); err == nil {
				t.Fatal("foreign/private authority input accepted")
			}
		})
	}
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedInput(path); err == nil {
		t.Fatal("broad credential input accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedInput(link); err == nil {
		t.Fatal("linked private input accepted")
	}
}
