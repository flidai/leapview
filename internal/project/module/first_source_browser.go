package module

import (
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
)

type FirstSourceCredentialSignal = projectsignals.FirstSourceCredentialSignal
type FirstSourceCredentialEnvelope = projectsignals.FirstSourceCredentialEnvelope
type ConnectionCredentialDraftSignal = projectsignals.ConnectionCredentialDraftSignal

// FirstSourceCredentialsPage receives the credential capability's command
// bindings from application composition; project UI does not own that service.
func FirstSourceCredentialsPage(state FirstSourceCredentialSignal, csrf string, provider webpage.Provider, bindings map[string]uicommand.Binding) g.Node {
	return projectui.FirstSourceCredentialsPage(state, csrf, provider, bindings)
}
