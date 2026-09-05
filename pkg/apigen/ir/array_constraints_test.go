package ir

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate_AcceptsArrayUnionWithoutDiscriminator(t *testing.T) {
	doc := Document{
		SchemaVersion: CurrentSchemaVersion,
		API:           API{BasePath: "/"},
		Info:          Info{Title: "Contracts", Version: "1.0.0"},
		Schemas: map[string]Schema{
			"AllowedValues": {Type: "union", OneOf: []SchemaRef{
				{Type: "array", Items: &SchemaRef{Type: "string"}},
				{Type: "array", Items: &SchemaRef{Type: "boolean"}},
			}},
		},
		Contracts: []Contract{{Name: "values", Schema: SchemaRef{Ref: "AllowedValues"}}},
	}
	require.NoError(t, Validate(doc))

	doc.Schemas["AllowedValues"] = Schema{Type: "union", OneOf: []SchemaRef{
		{Type: "array", Items: &SchemaRef{Type: "string"}},
		{Type: "string"},
	}}
	require.ErrorContains(t, Validate(doc), "array-only branches")
}

func TestValidateSchemaRefItemBounds(t *testing.T) {
	minItems, maxItems := 1, 4
	doc := Document{
		SchemaVersion: CurrentSchemaVersion,
		API:           API{BasePath: "/"},
		Info:          Info{Title: "Contracts", Version: "1.0.0"},
		Endpoints: []Endpoint{{Method: "post", Path: "/values", OperationID: "values",
			RequestBody: &RequestBody{Contents: []BodyContent{{ContentType: "application/json", BodyKind: "json",
				Schema: &SchemaRef{Type: "array", Items: &SchemaRef{Type: "string"}, MinItems: &minItems, MaxItems: &maxItems},
			}}},
			Responses: []Response{{StatusCode: 200, Description: "ok"}},
		}},
	}
	require.NoError(t, Validate(doc))

	negative := -1
	doc.Endpoints[0].RequestBody.Contents[0].Schema.MinItems = &negative
	require.ErrorContains(t, Validate(doc), "item bounds must be non-negative")

	tooSmall := 5
	doc.Endpoints[0].RequestBody.Contents[0].Schema.MinItems = &tooSmall
	doc.Endpoints[0].RequestBody.Contents[0].Schema.MaxItems = &maxItems
	require.ErrorContains(t, Validate(doc), "min_items must not exceed max_items")
}
