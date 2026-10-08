package configschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGeneratedCUESourceCacheIsBoundedAndReturnsCopies(t *testing.T) {
	kinds := []struct {
		kind  Kind
		cache *generatedCUESourceCache
	}{
		{KindConnection, &generatedConnectionCUESource},
		{KindSource, &generatedSourceCUESource},
		{KindModel, &generatedModelCUESource},
		{KindSemanticModel, &generatedSemanticCUESource},
	}

	var retainedBytes int
	for _, item := range kinds {
		source, err := generatedCUESourceForKind(item.kind)
		if err != nil {
			t.Fatalf("get %s CUE source: %v", item.kind, err)
		}
		if len(source) == 0 || source[len(source)-1] != '\n' {
			t.Fatalf("%s CUE source has invalid formatting: %q", item.kind, source)
		}
		original := bytes.Clone(source)
		source[0] ^= 0xff
		again, err := generatedCUESourceForKind(item.kind)
		if err != nil {
			t.Fatalf("get %s CUE source after mutating returned copy: %v", item.kind, err)
		}
		if !bytes.Equal(again, original) {
			t.Fatalf("mutating returned %s CUE bytes changed cached source", item.kind)
		}
		if !bytes.Equal(item.cache.content, original) {
			t.Fatalf("mutating returned %s CUE bytes changed retained source", item.kind)
		}
		retainedBytes += len(item.cache.content)
		t.Logf("retained %s CUE source: %d bytes", item.kind, len(item.cache.content))
	}
	if retainedBytes == 0 {
		t.Fatal("retained CUE source byte count is zero")
	}
	t.Logf("bounded retained CUE source total: %d bytes across %d kinds", retainedBytes, len(kinds))

	if _, err := generatedCUESourceForKind(KindPipeline); err == nil {
		t.Fatal("generated CUE source cache accepted unsupported Pipeline kind")
	}
}

func TestGeneratedCUEValidationIsIsolatedAcrossConcurrentCalls(t *testing.T) {
	fixtures := []struct {
		kind Kind
		doc  []byte
	}{
		{KindConnection, []byte(testConnectionYAML)},
		{KindSource, []byte(metadataSourceDocument)},
		{KindModel, []byte(metadataModelDocument)},
		{KindSemanticModel, semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales, finance]`)},
	}
	type preparedFixture struct {
		kind  Kind
		root  *yaml.Node
		value any
	}
	prepared := make([]preparedFixture, 0, len(fixtures))
	for _, fixture := range fixtures {
		root, err := parseResourceDocument(string(fixture.kind)+".yaml", fixture.doc)
		if err != nil {
			t.Fatalf("parse %s fixture: %v", fixture.kind, err)
		}
		normalized, err := NormalizeJSONDocument(string(fixture.kind)+".yaml", fixture.doc)
		if err != nil {
			t.Fatalf("normalize %s fixture: %v", fixture.kind, err)
		}
		value, err := decodeJSONForCUE(normalized)
		if err != nil {
			t.Fatalf("decode %s fixture: %v", fixture.kind, err)
		}
		prepared = append(prepared, preparedFixture{kind: fixture.kind, root: root, value: value})
	}

	const workersPerKind = 2
	const rounds = 2
	start := make(chan struct{})
	errors := make(chan error, len(prepared)*workersPerKind)
	var workers sync.WaitGroup
	for _, fixture := range prepared {
		for worker := 0; worker < workersPerKind; worker++ {
			fixture, worker := fixture, worker
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				filename := fmt.Sprintf("%s-worker-%d.yaml", fixture.kind, worker)
				for round := 0; round < rounds; round++ {
					value, err := cloneGeneratedCUEValue(fixture.value)
					if err != nil {
						errors <- fmt.Errorf("clone valid %s fixture: %w", fixture.kind, err)
						return
					}
					if err := validateGeneratedCUEAt(fixture.kind, filename, fixture.root, value); err != nil {
						errors <- fmt.Errorf("valid %s round %d: %w", fixture.kind, round, err)
						return
					}

					invalid, err := cloneGeneratedCUEValue(fixture.value)
					if err != nil {
						errors <- fmt.Errorf("clone invalid %s fixture: %w", fixture.kind, err)
						return
					}
					resource := invalid.(map[string]any)
					metadata := resource["metadata"].(map[string]any)
					metadata["id"] = 42
					err = validateGeneratedCUEAt(fixture.kind, filename, fixture.root, invalid)
					diagnostics := Diagnostics(err)
					if len(diagnostics) != 1 || diagnostics[0].File != filename || diagnostics[0].Code != "schema.generated" || diagnostics[0].FieldPath != "metadata.id" || diagnostics[0].Line == 0 || diagnostics[0].Column == 0 {
						errors <- fmt.Errorf("invalid %s round %d diagnostic = %#v, error = %v", fixture.kind, round, diagnostics, err)
						return
					}

					if err := validateGeneratedCUEAt(fixture.kind, filename, fixture.root, fixture.value); err != nil {
						errors <- fmt.Errorf("valid %s after invalid round %d: %w", fixture.kind, round, err)
						return
					}
				}
			}()
		}
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func cloneGeneratedCUEValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	clone, err := decodeJSONForCUE(encoded)
	if err != nil {
		return nil, err
	}
	return clone, nil
}
