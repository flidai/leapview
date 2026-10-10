package hostinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agentTransitionFixture(t *testing.T) (string, AgentCredentialTransition) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "agent-credential-transitions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := AgentTransitionFile{Version: 1, TransitionVersion: "0198f2c0-7c7a-7f00-8a11-000000000301", Nonce: strings.Repeat("a", 64), InstallationID: "instance-a", InstanceID: "product-instance-b", CustomerOwnerID: "customer-a", ExpectedRevision: 2, ActorID: "admin-a", LoginEmail: "admin@example.com", AdminPassword: "fixture-password", APIKey: "fixture-provider-key", Provider: AgentProviderSettings{Enabled: true, Model: "model-a", BaseURL: "https://provider.example/v1", APIMode: "responses"}}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	intent, err := ReadAgentTransitionIntent(root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	intent.ProviderAddress = "93.184.215.14"
	return root, intent
}

func TestAgentTransitionBindsExactPrivateFileAndRequest(t *testing.T) {
	root, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	before, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	r.AgentCredentialTransition = &intent
	after, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("transition did not bind request identity")
	}
	file, err := readBoundAgentTransition(root, intent)
	if err != nil {
		t.Fatal(err)
	}
	if file.APIKey != "fixture-provider-key" {
		t.Fatal("private export mismatch")
	}
	path := filepath.Join(root, "agent-credential-transitions", "fixture.json")
	raw, _ := os.ReadFile(path)
	raw = []byte(strings.Replace(string(raw), "fixture-provider-key", "changed-provider-key", 1))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundAgentTransition(root, intent); err == nil {
		t.Fatal("changed staging file accepted")
	}
	encoded, _ := json.Marshal(intent)
	if strings.Contains(string(encoded), "fixture-provider-key") || strings.Contains(string(encoded), "fixture-password") || strings.Contains(string(encoded), strings.Repeat("a", 64)) {
		t.Fatal("intent exposed credentials")
	}
}

func TestAgentTransitionRejectsUnsafeReferencePermissionsAndParentSymlink(t *testing.T) {
	root, _ := agentTransitionFixture(t)
	for _, reference := range []string{"", ".", "..", "../fixture", "/fixture", "fixture.json", "a b"} {
		if _, err := ReadAgentTransitionIntent(root, reference); err == nil {
			t.Fatalf("accepted reference %q", reference)
		}
	}
	path := filepath.Join(root, "agent-credential-transitions", "fixture.json")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentTransitionIntent(root, "fixture"); err == nil {
		t.Fatal("nonprivate file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentTransitionIntent(alias, "fixture"); err == nil {
		t.Fatal("symlinked parent accepted")
	}
}

func TestAgentTransitionRejectsInvalidOrForeignIntent(t *testing.T) {
	_, intent := agentTransitionFixture(t)
	for _, change := range []func(*AgentCredentialTransition){
		func(i *AgentCredentialTransition) { i.InstallationID = "another-instance" },
		func(i *AgentCredentialTransition) { i.FileDigest = "invalid" },
		func(i *AgentCredentialTransition) { i.Provider.BaseURL = "http://provider.example/v1" },
		func(i *AgentCredentialTransition) { i.Provider.BaseURL = "https://127.0.0.1/v1" },
		func(i *AgentCredentialTransition) { i.Provider.Enabled = false },
		func(i *AgentCredentialTransition) { i.ExpectedRevision = 9007199254740991 },
		func(i *AgentCredentialTransition) { i.Provider.BaseURL += "/" },
	} {
		r := nativeRequestFixture(t)
		next := intent
		change(&next)
		r.AgentCredentialTransition = &next
		if _, err := r.Identity(); err == nil {
			t.Fatal("unsafe transition accepted")
		}
	}
}

func TestAgentTransitionPreservesOpaquePasswordAndProviderLimits(t *testing.T) {
	root, intent := agentTransitionFixture(t)
	file, err := readBoundAgentTransition(root, intent)
	if err != nil {
		t.Fatal(err)
	}
	file.AdminPassword = "  fixture password  "
	path := filepath.Join(root, "agent-credential-transitions", "fixture.json")
	write := func() {
		raw, _ := json.Marshal(file)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	read, _, err := readAgentTransition(root, "fixture")
	if err != nil || read.AdminPassword != file.AdminPassword {
		t.Fatal("opaque password bytes were normalized")
	}
	file.AdminPassword = strings.Repeat("x", 1025)
	write()
	if _, _, err = readAgentTransition(root, "fixture"); err == nil {
		t.Fatal("oversize product password admitted")
	}
	file.AdminPassword = "fixture password"
	file.APIKey = strings.Repeat("x", 16385)
	write()
	if _, _, err = readAgentTransition(root, "fixture"); err == nil {
		t.Fatal("oversize provider key admitted")
	}
	file.APIKey = "fixture key"
	file.Provider.ReasoningEffort = "unknown"
	write()
	if _, _, err = readAgentTransition(root, "fixture"); err == nil {
		t.Fatal("unsupported provider effort admitted")
	}
}
