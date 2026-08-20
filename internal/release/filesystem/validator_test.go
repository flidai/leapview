package filesystem

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/publication"
)

func TestCompiledDashboardPublicationsEncodeForServingState(t *testing.T) {
	empty, err := compiledDashboardPublicationsJSON(nil)
	if err != nil || empty != "{}" {
		t.Fatalf("empty compiled dashboard publications = %q, %v", empty, err)
	}
	definitions := map[string]publication.Definition{
		"publication:website": {
			Name: "publication:website", Dashboard: "dashboard:sales", DefaultPage: "overview",
			AllowedOrigins: []string{"https://example.test"}, DependencyAssetIDs: []string{"dashboard:sales"},
			ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	encoded, err := compiledDashboardPublicationsJSON(definitions)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]publication.Definition
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded["publication:website"]; got.Dashboard != "dashboard:sales" || got.DefaultPage != "overview" {
		t.Fatalf("compiled dashboard publication = %#v", got)
	}
}
