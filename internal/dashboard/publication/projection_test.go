package publication

import "testing"

func TestMapProjectionPreservesBackendNeutralPublicationContract(t *testing.T) {
	row := Projection{
		ID: "publication", ProjectID: "project:demo", Name: "website", PublicID: "public",
		Dashboard: "dashboard:demo", DefaultPage: "overview", ConfigurationDigest: "sha256:digest",
		AllowedOriginsJSON: `["https://example.test"]`, DependencyAssetIDsJSON: `["asset:one"]`,
		Revision: 3, Configured: true, ServingStateID: "generation:one", CreatedAt: "created", UpdatedAt: "updated",
	}
	got, err := MapProjection(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != row.ID || got.ProjectID.String() != row.ProjectID || got.AllowedOrigins[0] != "https://example.test" || got.DependencyAssetIDs[0] != "asset:one" || got.Revision != row.Revision || got.ServingStateID != row.ServingStateID {
		t.Fatalf("mapped publication = %#v", got)
	}
}
