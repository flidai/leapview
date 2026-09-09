package compiler

import "testing"

// The discovery envelope must preserve generated contract metadata, not
// reject it before canonical inventory construction or admit it on other kinds.
func TestSourceRootContractMetadataUsesGeneratedKindAuthority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      string
		metadata  string
		wantError bool
	}{
		{"versioned source", "source", ", contract: {version: 1.0.0, compatibility: backward}", false},
		{"malformed version", "source", ", contract: {version: bogus, compatibility: backward}", true},
		{"unknown compatibility", "source", ", contract: {version: 1.0.0, compatibility: permissive}", true},
		{"connection is not contract bearing", "connection", ", contract: {version: 1.0.0, compatibility: backward}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connectionMetadata, sourceMetadata := "", ""
			if tc.kind == "connection" {
				connectionMetadata = tc.metadata
			} else {
				sourceMetadata = tc.metadata
			}
			files := map[string]string{
				"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse" + connectionMetadata + "}\nspec: {type: managed}\n",
				"sources/orders.yaml":        "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders" + sourceMetadata + "}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n",
			}
			_, err := Compile(writeSourceFixture(t, files))
			if (err != nil) != tc.wantError {
				t.Fatalf("Compile error=%v, wantError=%t", err, tc.wantError)
			}
		})
	}
}
