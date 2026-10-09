package credential

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestAgentValidationBindsFirstConfigurationRevisionZeroAndInstanceAuthority(t *testing.T) {
	f := newValidationFixture(t, map[string]string{"api_key": "agent-key-sentinel"})
	f.resource = Resource{ScopeKind: "agent", ResourceID: "deployment-a"}
	f.scope.Resource = f.resource
	f.scope.Provider = "openai"
	f.scope.Purpose = "agent-authentication"
	f.scopes.scope = f.scope
	f.target.Scope = f.scope
	f.target.BindingRevision = 0
	f.target.BindingID = "agent:deployment-a"
	f.probe.target = f.target
	f.version.Metadata.Binding = expectedEncryptionBinding("deployment-a", f.scope, f.version.Metadata.Binding.VersionID)
	f.repository.version = f.version
	f.keys.binding = f.version.Metadata.Binding
	receipt, err := f.service.ValidateDraft(t.Context(), "actor", f.resource, f.version.Metadata.Binding.VersionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BindingRevision != 0 || receipt.Binding.ScopeKind != "agent" || receipt.Binding.Provider != "openai" {
		t.Fatal("agent receipt lost exact scope or initial revision")
	}
	if len(f.authorizer.pairs) != 2 {
		t.Fatalf("authority rechecks=%d", len(f.authorizer.pairs))
	}
	for _, pair := range f.authorizer.pairs {
		if pair.Action != access.ActionPlatformSettingsUpdate || pair.Target.InstanceID != "deployment-a" {
			t.Fatalf("wrong agent permission=%+v", pair)
		}
	}
	if len(f.repository.audits) != 1 || f.repository.audits[0].ResourceKind != "instance" || f.repository.audits[0].ScopeID != "deployment-a" {
		t.Fatal("agent validation audit wrong")
	}
	if validCredentialRevision("connection", 0) {
		t.Fatal("agent initial revision exception weakened source binding CAS")
	}
}

func TestDisabledAgentCredentialAcceptsOnlyExplicitEmptyKey(t *testing.T) {
	for _, test := range []struct {
		name, kind, provider string
		fields               map[string]string
		allowed              bool
	}{
		{"disabled", "agent", "agent-disabled", map[string]string{"api_key": ""}, true},
		{"enabled empty", "agent", "openai", map[string]string{"api_key": ""}, false},
		{"disabled with key", "agent", "agent-disabled", map[string]string{"api_key": "secret"}, false},
		{"missing key", "agent", "agent-disabled", map[string]string{"other": ""}, false},
		{"source exception", "connection", "agent-disabled", map[string]string{"api_key": ""}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope := Scope{Resource: Resource{ScopeKind: test.kind}, Provider: test.provider}
			_, err := encodeFields(scope, test.fields)
			if (err == nil) != test.allowed {
				t.Fatalf("encode=%v", err)
			}
			data, _ := json.Marshal(test.fields)
			decoded, ok := decodeValidatedCredential(scope, data)
			clear(decoded)
			if ok != test.allowed {
				t.Fatalf("decode allowed=%t", ok)
			}
		})
	}
}
