package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/credential"
)

type firstSourceOperations struct {
	fakeOperations
	request *FirstSourceAdmissionRequest
}

func (o *firstSourceOperations) AdmitFirstSource(_ context.Context, request FirstSourceAdmissionRequest, _ io.Writer) error {
	o.request = &request
	return nil
}

func firstSourceIntent() credential.FirstSourceAdmissionIntent {
	return credential.FirstSourceAdmissionIntent{
		Version: 1, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000101", TargetID: "target:first",
		ProjectID: "finance", Environment: "prod", CustomerOwnerID: "customer:one", OperatorPrincipalID: "principal:operator",
		ConnectionID: "warehouse", BindingID: "binding:first", ExpectedPolicyRevision: 3,
		ExpectedPolicyDigest: "sha256:" + strings.Repeat("a", 64),
		Endpoint:             credential.FirstSourceEndpoint{Host: "postgres.internal", Port: 5432, Database: "analytics", TLSMode: "require"},
		CredentialReference:  credential.FirstSourceCredentialReference{ProjectID: "finance", Environment: "prod", SecretPath: "/customer/warehouse", SecretKey: "password"},
	}
}

func TestFirstSourceAdmissionCommandRequiresExactPrivateIntent(t *testing.T) {
	valid, err := json.Marshal(firstSourceIntent())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name            string
		body            []byte
		mode            os.FileMode
		apply, accepted bool
	}{
		{"preview", valid, 0600, false, true},
		{"apply", valid, 0600, true, true},
		{"public intent", valid, 0644, false, false},
		{"unknown credential fields", append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"password":"operator-secret"}`)...), 0600, true, false},
		{"duplicate operation", append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"operationId":"0198f2c0-7c7a-7f00-8a11-000000000102"}`)...), 0600, true, false},
		{"trailing document", append(append([]byte{}, valid...), []byte(`{}`)...), 0600, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "intent.json")
			if err := os.WriteFile(path, test.body, test.mode); err != nil {
				t.Fatal(err)
			}
			operations := &firstSourceOperations{}
			command := Command(t.Context(), operations)
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			args := []string{"credentials", "admit-first-source", "--intent", path}
			if test.apply {
				args = append(args, "--apply")
			}
			command.SetArgs(args)
			err := command.Execute()
			if test.accepted {
				if err != nil || operations.request == nil || operations.request.Apply != test.apply || operations.request.Intent.OperationID != firstSourceIntent().OperationID {
					t.Fatalf("admission=%v error=%v", operations.request, err)
				}
			} else if err == nil || operations.request != nil {
				t.Fatal("invalid admission intent reached operator")
			}
			if strings.Contains(output.String(), "operator-secret") {
				t.Fatal("credential request bytes leaked")
			}
		})
	}
}
