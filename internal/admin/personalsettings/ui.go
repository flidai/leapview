package personalsettings

import (
	"net/http"
	"time"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
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
	profileMutation := uiactions.CommandPost(accessgen.GenUIActionUpdateCurrentPrincipal(), path, "personalProfileCommand")
	profileRefresh := uiactions.QueryPost(path, "personalProfileCommand")
	return []g.Node{
		g.Attr("data-on:lv-personal-profile-command", "$personalProfileCommand = evt.detail; evt.detail.action == 'refresh' ? ("+profileRefresh+") : ("+profileMutation+")"),
		g.Attr("data-on:lv-personal-theme-command", "$personalThemeCommand = evt.detail; "+uiactions.CommandPost(accessgen.GenUIActionUpdateCurrentTheme(), path, "personalThemeCommand")),
		g.Attr("data-on:lv-personal-password-command", "$personalPasswordCommand = evt.detail; "+uiactions.CommandPost(accessgen.GenUIActionChangeCurrentPassword(), path, "personalPasswordCommand")),
		g.Attr("data-on:lv-personal-session-command", "$personalSessionCommand = evt.detail; "+uiactions.CommandPost(accessgen.GenUIActionRevokeCurrentSession(), path, "personalSessionCommand")),
		g.Attr("data-on:lv-personal-authoring-session-command", "$personalAuthoringSessionCommand = evt.detail; "+uiactions.CommandPost(accessgen.GenUIActionRevokeCurrentAuthoringSession(), path, "personalAuthoringSessionCommand")),
		g.Attr("data-on:lv-personal-token-command", "$personalTokenCommand = evt.detail; "+uiactions.CommandPostSwitchWithRevision("evt.detail.action", map[string]uicommand.Binding{
			"create": accessgen.GenUIActionCreateCurrentAPIToken(), "update": accessgen.GenUIActionUpdateCurrentAPIToken(), "rotate": accessgen.GenUIActionRotateCurrentAPIToken(), "revoke": accessgen.GenUIActionRevokeCurrentAPIToken(),
		}, path, `(evt.detail.action == 'update' || evt.detail.action == 'rotate') ? '"' + evt.detail.expectedModifiedAt + '"' : ''`, "personalTokenCommand")),
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

// UpdatesSignals retains a browser-owned one-time secret across an updates
// reconnect only while its mounted principal and displayed token are current.
// Commands continue to use BootstrapSignals so explicit clears remain effective.
func UpdatesSignals(r *http.Request, state Signal) map[string]any {
	signals := BootstrapSignals(state)
	if state.Active != "api-tokens" || state.Profile.ID == "" || state.Tokens.NewToken != nil || len(state.Tokens.Items) == 0 {
		return signals
	}
	// Decode identity and revision metadata only, never the browser's secret.
	var mounted struct {
		Settings struct {
			Active  string `json:"active"`
			Profile struct {
				ID string `json:"id"`
			} `json:"profile"`
			Tokens struct {
				Items []struct {
					ID         string `json:"id"`
					CreatedAt  string `json:"createdAt"`
					ModifiedAt string `json:"modifiedAt"`
				} `json:"items"`
			} `json:"tokens"`
		} `json:"personalSettings"`
	}
	if pagestream.ReadSignals(r, &mounted) != nil || mounted.Settings.Active != state.Active || mounted.Settings.Profile.ID != state.Profile.ID || len(mounted.Settings.Tokens.Items) == 0 {
		return signals
	}
	current, previous := state.Tokens.Items[0], mounted.Settings.Tokens.Items[0]
	if current.ID == "" || current.CreatedAt == "" || current.RevokedAt != "" || current.ID != previous.ID || current.CreatedAt != previous.CreatedAt || current.ModifiedAt != previous.ModifiedAt {
		return signals
	}
	if current.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339Nano, current.ExpiresAt)
		if err != nil || !time.Now().Before(expires) {
			return signals
		}
	}
	// The generated tokens payload omits absent newToken, preserving only the
	// already-mounted value. Keep the explicit avatar null in every refresh.
	signals["personalSettings"] = struct {
		Signal
		Profile personalProfileWire `json:"profile"`
	}{Signal: state, Profile: personalProfileWire{ProfileSignal: state.Profile, AvatarURL: state.Profile.AvatarURL}}
	return signals
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
