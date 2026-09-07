package configschema

import (
	"strings"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

const metadataSourceDocument = `apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:orders
  name: orders
  contract:
    version: 1.2.3-rc.1+build.5
    compatibility: backward
spec:
  connection: managed:local
  location: {type: path, path: orders.csv, format: csv}
  schema:
    mode: strict
    fields:
      order_id:
        datatype: String
        nullable: false
        tags: [identifier]
        criticalDataElement: true
        classification: pii
        authoritativeDefinitions:
          - type: businessDefinition
            url: https://example.test/definitions/order-id
        deprecation:
          since: 1.2.0
          reason: use order_key instead
          replacement: order_key
`

const metadataModelDocument = `apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
  contract:
    version: 2.0.0
    compatibility: backward
spec:
  definition: {type: direct, source: source:orders}
  entities:
    order: {type: primary, fields: [order_id]}
  grain: {entity: order}
  fields:
    order_id:
      datatype: String
      nullable: false
      tags: [identifier]
      criticalDataElement: true
      classification: pii
      authoritativeDefinitions:
        - type: transformationImplementation
          url: https://example.test/models/orders
      deprecation:
        since: 1.0.0
        reason: use order_key instead
        replacement: order_key
  checks:
    - id: order_id_not_null
      type: non_null
      field: order_id
      description: Order IDs must be present
      tags: [quality]
    - id: order_id_unique
      type: unique
      fields: [order_id]
      description: Order IDs must be unique
      tags: [quality]
    - id: order_status_allowed
      type: accepted_values
      field: status
      values: [open, closed]
      description: Status is a governed enum
      tags: [quality]
    - id: customer_exists
      type: relationship
      field: customer_id
      to: sales.customers.customer
      description: Customers must exist
      tags: [integrity]
    - id: row_count_positive
      type: row_count
      minimum: 1
      description: The model must not be empty
      tags: [freshness]
`

func TestContractMetadataAndGovernanceFieldsAreAccepted(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		doc  string
	}{
		{name: "source", kind: KindSource, doc: metadataSourceDocument},
		{name: "source urn", kind: KindSource, doc: strings.Replace(metadataSourceDocument, "https://example.test/definitions/order-id", "urn:example:order-id", 1)},
		{name: "source without optional contract", kind: KindSource, doc: withoutContract(metadataSourceDocument, "1.2.3-rc.1+build.5")},
		{name: "model", kind: KindModel, doc: metadataModelDocument},
		{name: "model without optional contract", kind: KindModel, doc: withoutContract(metadataModelDocument, "2.0.0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBytes(tc.kind, tc.name+".yaml", []byte(tc.doc)); err != nil {
				t.Fatalf("valid metadata rejected: %v", err)
			}
		})
	}
}

func withoutContract(document, version string) string {
	return strings.Replace(document, "  contract:\n    version: "+version+"\n    compatibility: backward\n", "", 1)
}

func TestDecodeResourceRetainsTypedMetadataAndGovernance(t *testing.T) {
	var source projectcontracts.Source
	if err := DecodeResource(KindSource, "source.yaml", []byte(metadataSourceDocument), &source); err != nil {
		t.Fatalf("DecodeResource(source): %v", err)
	}
	if source.Metadata.Contract == nil || source.Metadata.Contract.Version != "1.2.3-rc.1+build.5" || source.Metadata.Contract.Compatibility != "backward" {
		t.Fatalf("decoded source contract = %#v", source.Metadata.Contract)
	}
	strict, ok := source.Spec.Schema.Value.(*projectcontracts.SourceSchemaStrictVariant)
	if !ok {
		t.Fatalf("decoded source schema variant = %T, want strict", source.Spec.Schema.Value)
	}
	field := strict.Fields["order_id"]
	if field.Nullable == nil || *field.Nullable || field.CriticalDataElement == nil || !*field.CriticalDataElement || field.AuthoritativeDefinitions == nil || len(*field.AuthoritativeDefinitions) != 1 || field.Deprecation == nil {
		t.Fatalf("decoded source governance field = %#v", field)
	}

	var model projectcontracts.Model
	if err := DecodeResource(KindModel, "model.yaml", []byte(metadataModelDocument), &model); err != nil {
		t.Fatalf("DecodeResource(model): %v", err)
	}
	if model.Metadata.Contract == nil || model.Metadata.Contract.Version != "2.0.0" {
		t.Fatalf("decoded model contract = %#v", model.Metadata.Contract)
	}
	modelField := (*model.Spec.Fields)["order_id"]
	if modelField.Nullable == nil || *modelField.Nullable || modelField.Tags == nil || len(*modelField.Tags) != 1 || modelField.Deprecation == nil {
		t.Fatalf("decoded model governance field = %#v", modelField)
	}
	checks := *model.Spec.Checks
	if len(checks) != 5 {
		t.Fatalf("decoded model checks = %d, want 5", len(checks))
	}
	if variant, ok := checks[0].Value.(*projectcontracts.ModelCheckNonNullVariant); !ok || variant.ID != "order_id_not_null" || variant.Description == nil || variant.Tags == nil {
		t.Fatalf("decoded model check = %#v (%T)", checks[0].Value, checks[0].Value)
	}
}

func TestContractMetadataRejectsInvalidVersionURIAndUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{
			name: "numeric prerelease leading zero",
			doc:  strings.Replace(metadataSourceDocument, "1.2.3-rc.1+build.5", "1.2.3-01", 1),
		},
		{
			name: "invalid authoritative URI",
			doc:  strings.Replace(metadataSourceDocument, "https://example.test/definitions/order-id", "not a URI", 1),
		},
		{
			name: "unknown contract field",
			doc:  strings.Replace(metadataSourceDocument, "    compatibility: backward\n", "    compatibility: backward\n    unsupported: true\n", 1),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBytes(KindSource, tc.name+".yaml", []byte(tc.doc)); err == nil {
				t.Fatal("ValidateBytes accepted invalid contract metadata")
			}
		})
	}
}

func TestModelChecksRequireStableIDs(t *testing.T) {
	for _, id := range []string{
		"order_id_not_null",
		"order_id_unique",
		"order_status_allowed",
		"customer_exists",
		"row_count_positive",
	} {
		t.Run(id, func(t *testing.T) {
			doc := strings.Replace(metadataModelDocument, "    - id: "+id+"\n", "    -\n", 1)
			if err := ValidateBytes(KindModel, "model.yaml", []byte(doc)); err == nil {
				t.Fatal("ValidateBytes accepted a model check without an id")
			}
		})
	}
}

func TestModelChecksRejectInvalidIDs(t *testing.T) {
	for _, id := range []string{"1order_id", "order id", "order-id", "order.id"} {
		t.Run(id, func(t *testing.T) {
			doc := strings.Replace(metadataModelDocument, "order_id_not_null", id, 1)
			if err := ValidateBytes(KindModel, "model.yaml", []byte(doc)); err == nil {
				t.Fatal("ValidateBytes accepted a model check with an invalid id")
			}
		})
	}
}

func TestContractMetadataRemainsSourceAndModelScoped(t *testing.T) {
	connection := `apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:warehouse
  name: warehouse
spec: {type: managed}
`
	semanticModel := `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic-model:sales
  name: sales
spec:
  datasets: {orders: {model: orders}}
  metrics: {}
`
	for _, tc := range []struct {
		name string
		kind Kind
		doc  string
	}{
		{name: "connection baseline", kind: KindConnection, doc: connection},
		{name: "semantic model baseline", kind: KindSemanticModel, doc: semanticModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBytes(tc.kind, tc.name+".yaml", []byte(tc.doc)); err != nil {
				t.Fatalf("baseline without contract rejected: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		kind Kind
		doc  string
	}{
		{
			name: "connection",
			kind: KindConnection,
			doc:  strings.Replace(connection, "  name: warehouse\n", "  name: warehouse\n  contract: {version: 1.0.0, compatibility: backward}\n", 1),
		},
		{
			name: "semantic model",
			kind: KindSemanticModel,
			doc:  strings.Replace(semanticModel, "  name: sales\n", "  name: sales\n  contract: {version: 1.0.0, compatibility: backward}\n", 1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBytes(tc.kind, tc.name+".yaml", []byte(tc.doc)); err == nil {
				t.Fatal("ValidateBytes accepted contract metadata outside Source/Model")
			}
		})
	}
}
