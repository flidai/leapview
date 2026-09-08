package contractprojection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
	graph "github.com/flidai/leapview/internal/project/graph"
)

func TestSQLPublicationIdentitySurvivesImplicitQualifierRename(t *testing.T) {
	var previous []byte
	for _, name := range []string{"orders", "renamed_orders"} {
		g, err := graph.NewProjectGraph([]graph.Resource{{ID: "source:stable", Name: name, Kind: graph.KindSource}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, err := NewReferenceContext(g)
		if err != nil {
			t.Fatal(err)
		}
		var input contracts.Model
		raw := fmt.Sprintf(`{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders_model"},"spec":{"definition":{"type":"sql","sql":"SELECT %s.id FROM source.%s"},"entities":{"row":{"type":"primary","fields":["id"]}},"grain":{"entity":"row"},"fields":{"id":{"datatype":"Integer"}}}}`, name, name)
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			t.Fatal(err)
		}
		projection, err := ProjectModel(input, Contract{Version: "1.0.0", Compatibility: "backward"}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := CanonicalBytes(projection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeModelPublication(wire); err != nil {
			t.Fatalf("producer emitted invalid publication: %v", err)
		}
		digest, err := Digest(projection)
		if err != nil {
			t.Fatal(err)
		}
		readDigest, err := DigestModelPublication(wire)
		if err != nil || readDigest != digest {
			t.Fatalf("publication digest %q != producer digest %q: %v", readDigest, digest, err)
		}
		if previous != nil && !bytes.Equal(previous, wire) {
			t.Fatalf("dependency rename changed publication identity:\n%s\n%s", previous, wire)
		}
		previous = wire
	}
}
