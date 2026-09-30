package cli

import (
	"bytes"
	"context"
	"io"
	"testing"
)

type credentialSetupOperations struct {
	fakeOperations
	owner string
}

func (o *credentialSetupOperations) SetupCredentials(_ context.Context, request CredentialSetupRequest, _ io.Writer) error {
	o.owner = request.OwnerID
	return nil
}

func TestCredentialSetupRequiresExplicitOwner(t *testing.T) {
	for _, owner := range []string{"", " customer:one", "customer one", "customer:one\n", "customer:one"} {
		operations := &credentialSetupOperations{}
		command := Command(t.Context(), operations)
		command.SetOut(&bytes.Buffer{})
		command.SetArgs([]string{"credentials", "setup", "--owner", owner})
		err := command.Execute()
		if owner == "customer:one" {
			if err != nil || operations.owner != owner {
				t.Fatalf("owner=%q error=%v", operations.owner, err)
			}
		} else if err == nil || operations.owner != "" {
			t.Fatal("invalid declaration reached setup")
		}
	}
}
