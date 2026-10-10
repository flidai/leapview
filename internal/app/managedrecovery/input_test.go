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

func TestManagedInputModuleProviderRejectsCallerTools(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	_, closure, _ := managedClosureFixture(t)
	input := ManagedInput{InstanceHome: root, SchemaVersion: 1, Profile: providerrestore.ManagedLocalProfile, RecoverySetID: "set", OccurrenceID: "occurrence", ValidationAttemptID: "validation", Validator: "operator", Publisher: "publisher", Closure: closure}
	raw, _ := json.Marshal(input)
	var document map[string]any
	json.Unmarshal(raw, &document)
	postgres := document["postgres"].(map[string]any)
	postgres["provider"] = "module-owned"
	path := filepath.Join(root, "input.json")
	write := func() {
		raw, _ := json.Marshal(document)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := ReadManagedInput(path); err != nil {
		t.Fatalf("module-owned selector rejected: %v", err)
	}
	for _, field := range []string{"postgres", "pgControlData", "pgBackRest", "bubblewrap", "configFile", "configDigest", "serverCertificateFile", "serverCertificateDigest", "serverKeyFile", "serverKeyDigest"} {
		postgres[field] = "/caller/override"
		write()
		if _, err := ReadManagedInput(path); err == nil {
			t.Fatalf("module-owned accepted caller field %s", field)
		}
		postgres[field] = ""
	}
	postgres["provider"] = "unknown"
	write()
	if _, err := ReadManagedInput(path); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
