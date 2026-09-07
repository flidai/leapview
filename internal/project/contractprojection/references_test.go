package contractprojection

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/modelsql"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestReferenceContextResolvesIDAndNameWithExpectedKind(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		reference string
		expected  projectgraph.Kind
		want      projectgraph.ResourceID
	}{
		{name: "explicit id", reference: "model:orders", expected: projectgraph.KindModel, want: "model:orders"},
		{name: "symbolic name", reference: "orders_model", expected: projectgraph.KindModel, want: "model:orders"},
		{name: "trimmed name", reference: " orders ", expected: projectgraph.KindSource, want: "source:orders"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := context.ResolveReference(test.reference, test.expected)
			if err != nil || got != test.want {
				t.Fatalf("ResolveReference(%q, %q) = %q, %v; want %q", test.reference, test.expected, got, err, test.want)
			}
		})
	}
}

func TestReferenceContextRejectsMissingAndWrongKind(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := context.ResolveReference("missing", projectgraph.KindModel); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing reference error = %v", err)
	}
	if _, err := context.ResolveReference("orders", projectgraph.KindModel); err == nil || !strings.Contains(err.Error(), "want model") {
		t.Fatalf("wrong-kind reference error = %v", err)
	}
}

func TestReferenceContextRejectsAmbiguousGraphNames(t *testing.T) {
	_, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
	}, nil)
	if !errors.Is(err, projectgraph.ErrDuplicateName) {
		t.Fatalf("duplicate graph name error = %v, want ErrDuplicateName", err)
	}
}

func TestReferenceContextStableAcrossGraphOrderAndRenames(t *testing.T) {
	first, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "renamed_orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "renamed_model"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstContext, err := NewReferenceContext(first)
	if err != nil {
		t.Fatal(err)
	}
	secondContext, err := NewReferenceContext(second)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := firstContext.ResolveReference("orders_model", projectgraph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := secondContext.ResolveReference("model:orders", projectgraph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatalf("graph order/name change changed stable ID: %q != %q", firstID, secondID)
	}
	if _, err := secondContext.ResolveReference("orders_model", projectgraph.KindModel); err == nil {
		t.Fatal("old symbolic name unexpectedly resolved after rename")
	}
}

func TestSQLProjectionUsesStableIDsAcrossDependencyRenames(t *testing.T) {
	firstGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "renamed_model"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "renamed_orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewReferenceContext(firstGraph)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewReferenceContext(secondGraph)
	if err != nil {
		t.Fatal(err)
	}
	left, err := modelsql.CanonicalProjectionWithReferences(`SELECT model.orders_model.id FROM source.orders`, first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := modelsql.CanonicalProjectionWithReferences(`SELECT model.renamed_model.id FROM source.renamed_orders`, second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(*left), []byte(*right)) {
		t.Fatalf("renaming graph dependencies changed SQL identity:\n%s\n%s", *left, *right)
	}
	for _, symbolic := range []string{"orders_model", "renamed_model", "renamed_orders"} {
		if strings.Contains(*left, symbolic) {
			t.Fatalf("canonical SQL retained symbolic reference %q: %s", symbolic, *left)
		}
	}
}

func TestResolveModelMemberReferenceUsesStableIDAndPreservesField(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model:customers", Kind: projectgraph.KindModel, Name: "customers"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveModelMemberReference(&context, "customers.customer_id", "relationship target")
	if err != nil {
		t.Fatal(err)
	}
	if got != "model:customers.customer_id" {
		t.Fatalf("resolved model member reference = %q, want model:customers.customer_id", got)
	}
	if _, err := resolveModelMemberReference(&context, "customers", "relationship target"); err == nil {
		t.Fatal("model reference without a target field was accepted")
	}
}
