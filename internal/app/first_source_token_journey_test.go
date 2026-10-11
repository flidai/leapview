package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/admin/personalsettings"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func (b *firstSourceJourneyBrowser) issueFirstSourceToken(t *testing.T) string {
	t.Helper()
	pairs, err := access.InitialProjectPublisherPermissions(projectgraph.ResourceID(sourceJourneyProject))
	require.NoError(t, err)
	resource, err := access.NewResourceRef("connection:warehouse", projectgraph.KindConnection)
	require.NoError(t, err)
	for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse} {
		pair, pairErr := access.NewExactPermissionPair(action, sourceJourneyProject, resource)
		require.NoError(t, pairErr)
		pairs = append(pairs, pair)
	}
	encoded, err := json.Marshal(pairs)
	require.NoError(t, err)
	var signals []personalsettings.PermissionPairSignal
	require.NoError(t, json.Unmarshal(encoded, &signals))
	foreign := append([]personalsettings.PermissionPairSignal(nil), signals...)
	foreignResource := "connection:other"
	foreign[len(foreign)-2].Target.ResourceID = &foreignResource
	denied, err := json.Marshal(map[string]any{"personalTokenCommand": personalsettings.TokenCommand{Action: "create", Name: "foreign-source-denied", Permissions: foreign}})
	require.NoError(t, err)
	result := b.request(t, http.MethodPost, "/admin/personal-settings/command?section=api-tokens", "createCurrentAPIToken", "application/json", strings.NewReader(string(denied)))
	require.Equal(t, http.StatusBadRequest, result.Code, "foreign connection must not borrow first-source authority")
	response := b.command(t, "/admin/personal-settings/command?section=api-tokens", "createCurrentAPIToken", map[string]any{"personalTokenCommand": personalsettings.TokenCommand{Action: "create", Name: "first-source-supported-publisher", Permissions: signals, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}})
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "data: signals "); ok {
			var envelope struct {
				PersonalSettings personalsettings.Signal `json:"personalSettings"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &envelope))
			if envelope.PersonalSettings.Tokens.NewToken != nil {
				return *envelope.PersonalSettings.Tokens.NewToken
			}
		}
	}
	t.Fatal("personal settings did not return the newly issued token")
	return ""
}
