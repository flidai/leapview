package contractprojection

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	modelsql "github.com/flidai/leapview/internal/analytics/modelsql"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDecodeModelPublicationValidatesClosedSQLProjection(t *testing.T) {
	wire := sqlModelPublication(t, `SELECT id FROM source.orders`)
	var publication map[string]any
	if err := json.Unmarshal(wire, &publication); err != nil {
		t.Fatal(err)
	}
	definition := publication["contract"].(map[string]any)["definition"].(map[string]any)

	tests := map[string]string{
		"null statements":   `{"statements":null}`,
		"null statement":    `{"statements":[null]}`,
		"unexpected field":  `{"statements":[{"kind":"select","unexpected":true}]}`,
		"missing field":     `{"statements":[{"kind":"select"}]}`,
		"invalid node type": `{"statements":["select"]}`,
		"invalid node kind": `{"statements":[{"kind":"bogus"}]}`,
		"wrong-kind field":  `{"statements":[{"kind":"select","type":"SELECT_NODE","aggregateHandling":"STANDARD_HANDLING","select":[{"kind":"star","class":"STAR","type":"STAR","name":"not-allowed"}],"from":{"kind":"empty","type":"EMPTY"}}]}`,
	}
	for name, ast := range tests {
		t.Run(name, func(t *testing.T) {
			definition["sqlAst"] = ast
			mutated, err := json.Marshal(publication)
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := normalizeJSONStrings(mutated)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := canonicalizeRFC8785(normalized)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeModelPublication(canonical); err == nil {
				t.Fatal("malformed SQL projection was accepted")
			}
			if _, err := DigestModelPublication(canonical); err == nil {
				t.Fatal("DigestModelPublication accepted malformed SQL projection")
			}
		})
	}

	// Keep the public validator directly covered as well: this is the API used
	// by publication decoding and is useful to callers that only have sqlAst.
	definition["sqlAst"] = `{"statements":[{"kind":"select"}]}`
	if err := modelsql.ValidateCanonicalProjection(definition["sqlAst"].(string)); err == nil {
		t.Fatal("missing required select fields were accepted by validator")
	}
	if err := modelsql.ValidateCanonicalProjection(`{"statements":[{"kind":"select","kind":"select"}]}`); err == nil {
		t.Fatal("duplicate canonical SQL projection keys were accepted")
	}
}

func TestDecodeModelPublicationAcceptsRepresentativeAdmittedSQL(t *testing.T) {
	queries := []string{
		`SELECT id FROM source.orders`,
		`WITH input AS (VALUES (1, 'one'), (2, 'two')) SELECT * FROM input`,
		`SELECT CAST(amount AS DECIMAL(38, 2)) AS exact_amount, TRY_CAST(amount AS INTEGER) AS integer_amount FROM source.orders`,
		`SELECT CAST(amount AS JSON) AS payload FROM source.orders`,
		`SELECT rank() OVER (PARTITION BY customer_id ORDER BY order_id ROWS BETWEEN 2 PRECEDING AND CURRENT ROW) AS rank_value, lag(amount, 1, 0) OVER (ORDER BY order_id) AS previous_amount FROM source.orders`,
		`SELECT id FROM source.orders UNION ALL SELECT id FROM source.payments ORDER BY id LIMIT 10`,
		`SELECT CASE WHEN amount > 0 THEN 'positive' ELSE 'zero' END AS bucket, count(*) AS n FROM source.orders GROUP BY bucket`,
		`SELECT o.id FROM source.orders o JOIN source.payments p USING (id)`,
		`SELECT id FROM source.orders WHERE id IN (SELECT id FROM source.payments)`,
		`SELECT id FROM source.orders WHERE amount BETWEEN 1 AND 2`,
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			wire := sqlModelPublication(t, query)
			if _, err := DecodeModelPublication(wire); err != nil {
				t.Fatalf("producer emitted invalid SQL publication: %v", err)
			}
		})
	}
}

func sqlModelPublication(t *testing.T, sqlText string) []byte {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "source:orders", Name: "orders", Kind: projectgraph.KindSource},
		{ID: "source:payments", Name: "payments", Kind: projectgraph.KindSource},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	var input projectcontracts.Model
	raw := fmt.Sprintf(`{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders_model"},"spec":{"definition":{"type":"sql","sql":%q},"entities":{"row":{"type":"primary","fields":["id"]}},"grain":{"entity":"row"},"fields":{"id":{"datatype":"Integer"}}}}`, sqlText)
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &input); err != nil {
		t.Fatal(err)
	}
	projection, err := ProjectModel(input, Contract{Version: "1.0.0", Compatibility: "backward"}, context)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := CanonicalBytes(projection)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}
