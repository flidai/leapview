package contractimport

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestValidateRejectsMissingImportedModels(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Consumer"},
		Schemas: map[string]ir.Schema{
			"Envelope": {Type: "object", Namespace: "Consumer", Properties: map[string]ir.SchemaProperty{
				"visual": {Schema: ir.SchemaRef{Ref: "MissingVisual"}},
			}},
		},
	}
	require.EqualError(t, Bindings{}.Validate(doc), `schema "Envelope" references unavailable exported model "MissingVisual"`)
}

func TestValidateRejectsExternalToLocalCycles(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Consumer"},
		Schemas: map[string]ir.Schema{
			"Envelope": {Type: "object", Namespace: "Consumer"},
			"Visual": {Type: "object", Namespace: "Producer", Properties: map[string]ir.SchemaProperty{
				"owner": {Schema: ir.SchemaRef{Ref: "Envelope"}},
			}},
		},
	}
	imports := Bindings{"Producer": {GoPackage: "example.com/producer", GoAlias: "producer"}}
	require.EqualError(t, imports.Validate(doc), `contract import cycle: external schema "Visual" references local schema "Envelope"`)
}

func TestValidateRejectsImportedUnionVariants(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Consumer"},
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", Namespace: "Consumer", OneOf: []ir.SchemaRef{{Ref: "PointVisual"}}, Discriminator: &ir.Discriminator{PropertyName: "kind", Mapping: map[string]string{"point": "PointVisual"}}},
			"PointVisual": {Type: "object", Namespace: "Producer"},
		},
	}
	imports := Bindings{"Producer": {GoPackage: "example.com/producer", GoAlias: "producer"}}
	require.EqualError(t, imports.Validate(doc), `local union "Visual" cannot use imported variant "PointVisual"`)
}

func TestValidateAllowsCoalescedNamespacesWithIdenticalBindings(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Consumer"},
		Schemas: map[string]ir.Schema{
			"Principal": {Type: "object", Namespace: "Producer.Access"},
			"Session":   {Type: "object", Namespace: "Producer.Sessions"},
		},
	}
	binding := Binding{GoPackage: "example.com/identity", GoAlias: "identity"}
	imports := Bindings{
		"Producer.Access":   binding,
		"Producer.Sessions": binding,
	}

	require.NoError(t, imports.Validate(doc))
}

func TestValidateRejectsInconsistentBindingsForOneGoPackage(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Consumer"},
		Schemas: map[string]ir.Schema{
			"Principal": {Type: "object", Namespace: "Producer.Access"},
			"Session":   {Type: "object", Namespace: "Producer.Sessions"},
		},
	}
	imports := Bindings{
		"Producer.Access":   {GoPackage: "example.com/identity", GoAlias: "identity"},
		"Producer.Sessions": {GoPackage: "example.com/identity", GoAlias: "sessions"},
	}

	require.ErrorContains(t, imports.Validate(doc), `share Go package "example.com/identity" with inconsistent bindings`)
}

func TestResolveSupportsExactNamespaceBindingsForPackagePlans(t *testing.T) {
	binding := Binding{
		GoPackage: "example.com/root", GoAlias: "rootapi", ExactNamespace: true,
	}
	imports := Bindings{"ExampleAPI": binding}

	namespace, got, ok := imports.Resolve("ExampleAPI")
	require.True(t, ok)
	require.Equal(t, "ExampleAPI", namespace)
	require.Equal(t, binding, got)

	_, _, ok = imports.Resolve("ExampleAPI.Agent")
	require.False(t, ok)
}
