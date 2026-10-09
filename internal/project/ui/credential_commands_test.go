package ui

import (
	"bytes"
	"strings"
	"testing"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	g "maragu.dev/gomponents"
)

func TestCredentialBridgeUsesNonReplayableCommandsAndClearsSecretSignals(t *testing.T) {
	connection := (&analyticsmodule.Module{}).ConnectionUICommandBindings()
	bindings := credentialmodule.CredentialBrowserBindings()
	bridge := connectionAdministrationRouteBridge(ConnectionCommandBindings{
		Create: connection.Create, Update: connection.Update, Refresh: connection.Refresh,
		Enable: connection.Enable, Disable: connection.Disable, Credentials: bindings,
	})
	var buffer bytes.Buffer
	if err := g.El("div", bridge...).Render(&buffer); err != nil {
		t.Fatal(err)
	}
	markup := buffer.String()
	for _, binding := range bindings {
		if !strings.Contains(markup, binding.OperationID()) {
			t.Fatalf("missing UI command %s", binding.OperationID())
		}
	}
	if strings.Count(markup, "nonReplayableHeaders") != len(bindings) || strings.Count(markup, "retryMaxCount: 0") != len(bindings) ||
		!strings.Contains(markup, "finally {") || !strings.Contains(markup, "$connectionAdmin.credentials.command.password =") ||
		!strings.Contains(markup, "evt.detail.password =") {
		t.Fatalf("credential bridge permits replay or retains secret signals: %s", markup)
	}
}
