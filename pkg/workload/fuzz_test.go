package workload

import (
	"context"
	"encoding/hex"
	"testing"
)

func qualificationFuzzString(data []byte, max int) string {
	if len(data) > max {
		data = data[:max]
	}
	return hex.EncodeToString(data)
}

func FuzzQualificationConfigValidation(f *testing.F) {
	f.Add([]byte("class"), uint8(2), int8(1))
	f.Add([]byte{}, uint8(1), int8(0))
	f.Fuzz(func(t *testing.T, data []byte, classes uint8, running int8) {
		if len(data) > 32 {
			data = data[:32]
		}
		classCount := int(classes%4) + 1
		ordered := make([]Class, classCount)
		policies := make(map[Class]Policy, classCount)
		for i := range ordered {
			name := Class("class-" + qualificationFuzzString(data, 24) + string(rune('a'+i)))
			ordered[i] = name
			policies[name] = Policy{MaximumRunning: 1, MaximumQueued: 2}
		}
		maximumRunning := int(running)
		if maximumRunning < 1 {
			maximumRunning = 1
		}
		if maximumRunning > 8 {
			maximumRunning = 8
		}
		_ = (Config{Classes: ordered, Policies: policies, MaximumRunning: maximumRunning, MaximumQueued: 8}).Validate()
	})
}

func FuzzQualificationIdentityCanonicalization(f *testing.F) {
	f.Add([]byte("principal"), []byte("group"))
	f.Add([]byte{}, []byte{})
	f.Fuzz(func(t *testing.T, principal, group []byte) {
		if len(principal) > 32 {
			principal = principal[:32]
		}
		if len(group) > 32 {
			group = group[:32]
		}
		identity := Identity{PrincipalID: qualificationFuzzString(principal, 32), GroupIDs: []string{
			qualificationFuzzString(group, 32), qualificationFuzzString(group, 32), "second",
		}}
		canonical, err := identity.Canonicalize()
		if err != nil {
			return
		}
		if len(canonical.GroupIDs) != 2 || canonical.GroupIDs[0] > canonical.GroupIDs[1] {
			t.Fatalf("non-canonical groups: %v", canonical.GroupIDs)
		}
	})
}

func FuzzQualificationRequestValidation(f *testing.F) {
	f.Add([]byte("principal"), []byte("class"), []byte("operation"), int64(1))
	f.Add([]byte{}, []byte{}, []byte{}, int64(0))
	f.Fuzz(func(t *testing.T, principal, class, operation []byte, memory int64) {
		request := Request{
			PrincipalID:          qualificationFuzzString(principal, 32),
			Class:                Class(qualificationFuzzString(class, 32)),
			Operation:            qualificationFuzzString(operation, 32),
			EstimatedMemoryBytes: memory,
			GroupIDs:             []string{"g", qualificationFuzzString(class, 32), "g"},
		}
		canonical, err := request.Canonicalize()
		if err != nil {
			return
		}
		if canonical.EstimatedMemoryBytes <= 0 || len(canonical.GroupIDs) != 2 {
			t.Fatalf("invalid canonical request: %+v", canonical)
		}
	})
}

func FuzzQualificationBoundedSchedulerOperation(f *testing.F) {
	f.Add([]byte("bounded workload"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 48 {
			data = data[:48]
		}
		classCount := 1
		if len(data) > 0 {
			classCount = int(data[0]%4) + 1
		}
		classes := make([]Class, classCount)
		policies := make(map[Class]Policy, classCount)
		for i := range classes {
			classes[i] = Class("fuzz-class-" + string(rune('a'+i)))
			policies[classes[i]] = Policy{MaximumRunning: 2, MaximumQueued: 8}
		}
		controller, err := New(Config{Classes: classes, Policies: policies, MaximumRunning: 2, MaximumQueued: 8})
		if err != nil {
			t.Fatalf("bounded config unexpectedly rejected: %v", err)
		}
		for i, value := range data {
			ctx := context.Background()
			var cancel context.CancelFunc
			if value&1 == 1 {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			request := Request{Class: classes[i%len(classes)], PrincipalID: "fuzz-principal", Operation: "fuzz.step", EstimatedMemoryBytes: int64(value%4) + 1}
			lease, err := controller.Acquire(ctx, request)
			if lease != nil {
				lease.Release()
			}
			if cancel != nil {
				cancel()
			}
			if lease == nil && err == nil {
				t.Fatal("Acquire returned nil lease and nil error")
			}
		}
		controller.Close()
	})
}
