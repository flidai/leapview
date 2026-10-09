package cli

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"
)

type rotationOperationsStub struct {
	fakeOperations
	batch int
}

func (o *rotationOperationsStub) RotateCredentials(_ context.Context, r CredentialRotationRequest, _ io.Writer) error {
	o.batch = r.BatchSize
	return nil
}
func TestCredentialRotationCommandBoundsBatches(t *testing.T) {
	for _, batch := range []int{-1, 0, 1, 100, 101} {
		operations := &rotationOperationsStub{}
		command := Command(t.Context(), operations)
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"credentials", "rotate", "--batch-size", strconv.Itoa(batch)})
		err := command.Execute()
		if batch >= 1 && batch <= 100 {
			if err != nil || operations.batch != batch {
				t.Fatalf("bounded rotation not dispatched: %v", err)
			}
		} else if err == nil || operations.batch != 0 {
			t.Fatal("unbounded rotation dispatched")
		}
	}
}
