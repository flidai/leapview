package personalsettings

import (
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	g "maragu.dev/gomponents"
)

// Component is the page-local Lit host. The parent admin shell can place it
// in the profile/settings branch without importing any access transport code.
func Component() g.Node {
	return g.El("lv-personal-settings", g.Attr("slot", "personal-settings"))
}

// CommandAttributes wires browser events to Datastar commands. All mutations
// are represented as typed signals; the component itself never performs a
// settings GET request.
func CommandAttributes(path string) []g.Node {
	return []g.Node{
		g.Attr("data-on:lv-personal-profile-command", "$personalProfileCommand = evt.detail; "+uiactions.UncontractedMutationPost(path, "personalProfileCommand")),
		g.Attr("data-on:lv-personal-theme-command", "$personalThemeCommand = evt.detail; "+uiactions.UncontractedMutationPost(path, "personalThemeCommand")),
		g.Attr("data-on:lv-personal-password-command", "$personalPasswordCommand = evt.detail; "+uiactions.UncontractedMutationPost(path, "personalPasswordCommand")),
		g.Attr("data-on:lv-personal-session-command", "$personalSessionCommand = evt.detail; "+uiactions.UncontractedMutationPost(path, "personalSessionCommand")),
		g.Attr("data-on:lv-personal-authoring-session-command", "$personalAuthoringSessionCommand = evt.detail; "+uiactions.UncontractedMutationPost(path, "personalAuthoringSessionCommand")),
		g.Attr("data-on:lv-personal-token-command", "$personalTokenCommand = evt.detail; "+uiactions.UncontractedMutationPost(path, "personalTokenCommand")),
	}
}

func BootstrapSignals(state Signal) map[string]any {
	return map[string]any{
		"personalSettings":                personalSettingsPayload(state),
		"personalProfileCommand":          ProfileCommand{},
		"personalThemeCommand":            ThemeCommand{},
		"personalPasswordCommand":         PasswordCommand{},
		"personalSessionCommand":          SessionCommand{},
		"personalAuthoringSessionCommand": AuthoringSessionCommand{},
		"personalTokenCommand":            TokenCommand{},
	}
}

// Generated optional fields use omitempty, while Datastar merge patches need
// an explicit null to clear an avatar or one-time token from browser state.
// These narrow wire wrappers preserve that transport behavior without
// duplicating the generated signal models.
type personalSettingsWire struct {
	Signal
	Profile personalProfileWire `json:"profile"`
	Tokens  personalTokensWire  `json:"tokens"`
}

type personalProfileWire struct {
	ProfileSignal
	AvatarURL *string `json:"avatarUrl"`
}

type personalTokensWire struct {
	TokensSignal
	NewToken *string `json:"newToken"`
}

func personalSettingsPayload(state Signal) personalSettingsWire {
	return personalSettingsWire{
		Signal:  state,
		Profile: personalProfileWire{ProfileSignal: state.Profile, AvatarURL: state.Profile.AvatarURL},
		Tokens:  personalTokensWire{TokensSignal: state.Tokens, NewToken: state.Tokens.NewToken},
	}
}
