package graph

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
)

func TestPublishedGraphValidationDoesNotReplaceOwnedMetadata(t *testing.T) {
	project, err := NewProjectGraph([]Resource{{ID: "orders", Kind: KindModel, Name: "orders", Metadata: Metadata{Tags: []string{"z", "a"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project.resources[0].Metadata.Tags, []string{"a", "z"}) {
		t.Fatal("constructor did not canonicalize its private metadata")
	}
	owned := &project.resources[0].Metadata.Tags[0]
	canonical, digest := project.CanonicalBytes(), project.Digest()
	if err := project.Validate(); err != nil {
		t.Fatal(err)
	}
	if &project.resources[0].Metadata.Tags[0] != owned {
		t.Fatal("validation replaced published graph metadata")
	}
	if !bytes.Equal(project.CanonicalBytes(), canonical) || project.Digest() != digest {
		t.Fatal("validation changed canonical graph identity")
	}
}

func TestPublishedGraphSupportsConcurrentValidationAndReaders(t *testing.T) {
	project, err := NewProjectGraph(portableResources(), []Edge{{From: "model_orders", To: "source_orders"}})
	if err != nil {
		t.Fatal(err)
	}
	var ready, finished sync.WaitGroup
	ready.Add(8)
	finished.Add(8)
	start := make(chan struct{})
	errors := make([]error, 8)
	for index := range 8 {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			for range 20 {
				if err := project.Validate(); err != nil {
					errors[index] = err
					return
				}
				_ = project.Resources()
				_ = project.Edges()
			}
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	for _, err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}
