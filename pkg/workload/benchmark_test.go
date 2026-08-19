package workload

import (
	"context"
	"strconv"
	"testing"
)

func benchmarkWorkloadController(b *testing.B, classes []Class, maximumRunning, maximumQueued int) *Controller {
	b.Helper()
	policies := make(map[Class]Policy, len(classes))
	for _, class := range classes {
		policies[class] = Policy{MaximumRunning: maximumRunning, MaximumQueued: maximumQueued}
	}
	controller, err := New(Config{Classes: classes, Policies: policies, MaximumRunning: maximumRunning, MaximumQueued: maximumQueued})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(controller.Close)
	return controller
}

func benchmarkAcquireRelease(b *testing.B, controller *Controller, request func(int) Request) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			lease, err := controller.Acquire(context.Background(), request(i))
			if err != nil {
				b.Fatalf("Acquire: %v", err)
			}
			lease.Release()
			i++
		}
	})
}

func BenchmarkEmptyWorkload(b *testing.B) {
	controller := benchmarkWorkloadController(b, []Class{"empty"}, 64, 64)
	benchmarkAcquireRelease(b, controller, func(int) Request {
		return Request{Class: "empty", PrincipalID: "p", Operation: "benchmark.empty", EstimatedMemoryBytes: 1}
	})
}

func BenchmarkContendedWorkload(b *testing.B) {
	controller := benchmarkWorkloadController(b, []Class{"contended"}, 1, 4096)
	benchmarkAcquireRelease(b, controller, func(i int) Request {
		return Request{Class: "contended", PrincipalID: "p" + strconv.Itoa(i%8), Operation: "benchmark.contended", EstimatedMemoryBytes: 1}
	})
}

func BenchmarkManyClassesWorkload(b *testing.B) {
	classes := make([]Class, 32)
	for i := range classes {
		classes[i] = Class("class-" + strconv.Itoa(i))
	}
	controller := benchmarkWorkloadController(b, classes, 16, 4096)
	benchmarkAcquireRelease(b, controller, func(i int) Request {
		return Request{Class: classes[i%len(classes)], PrincipalID: "p", Operation: "benchmark.classes", EstimatedMemoryBytes: 1}
	})
}

func BenchmarkManyActorsWorkload(b *testing.B) {
	controller := benchmarkWorkloadController(b, []Class{"actors"}, 16, 4096)
	benchmarkAcquireRelease(b, controller, func(i int) Request {
		return Request{Class: "actors", PrincipalID: "principal-" + strconv.Itoa(i%256), Operation: "benchmark.actors", EstimatedMemoryBytes: 1}
	})
}

func BenchmarkManyGroupsWorkload(b *testing.B) {
	controller := benchmarkWorkloadController(b, []Class{"groups"}, 16, 4096)
	groups := make([]string, 32)
	for i := range groups {
		groups[i] = "group-" + strconv.Itoa(i)
	}
	benchmarkAcquireRelease(b, controller, func(int) Request {
		return Request{Class: "groups", PrincipalID: "p", GroupIDs: groups, Operation: "benchmark.groups", EstimatedMemoryBytes: 1}
	})
}
