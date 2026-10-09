package ui

import (
	"bytes"
	"strings"
	"testing"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestFirstSourcePageKeepsSecretCommandsExplicitAndNonReplayable(t *testing.T) {
	var page bytes.Buffer
	err := FirstSourceCredentialsPage(projectsignals.FirstSourceCredentialSignal{ConnectionID: "connection:warehouse", Host: "db.example.test", Database: "warehouse", SourceIdentity: "reader", TargetRevision: 1}, "csrf", nil, credentialmodule.FirstSourceBrowserBindings()).Render(&page)
	if err != nil {
		t.Fatal(err)
	}
	markup := page.String()
	for _, binding := range credentialmodule.FirstSourceBrowserBindings() {
		if !strings.Contains(markup, binding.OperationID()) {
			t.Fatalf("missing canonical command %s", binding.OperationID())
		}
	}
	if strings.Count(markup, "retryMaxCount: 0") != 5 || !strings.Contains(markup, "finally {") || !strings.Contains(markup, "$firstSourceCredentials.command.password =") || !strings.Contains(markup, "evt.detail.password =") {
		t.Fatal("secret-bearing commands must never replay or retain secret signals")
	}
	if strings.Contains(markup, "data-signals=") {
		t.Fatal("page must not embed credential state")
	}
}
