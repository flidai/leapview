package graph

import (
	"sync"
	"testing"
)

func TestImmutableGraphValidationIsSafeForConcurrentRuntimeReaders(t *testing.T) {
	graph, err := NewProjectGraph(portableResources(), []Edge{{From: "source_orders", To: "connection_warehouse"}})
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest := string(graph.CanonicalBytes()), graph.Digest()
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			for range 25 {
				if err := graph.Validate(); err != nil {
					t.Error(err)
				}
				for _, resource := range graph.Resources() {
					if _, ok := graph.Resource(resource.ID); !ok {
						t.Error("validated graph lost resource")
					}
				}
			}
		})
	}
	close(start)
	workers.Wait()
	if string(graph.CanonicalBytes()) != canonical || graph.Digest() != digest {
		t.Fatal("validation changed published graph identity")
	}
}
