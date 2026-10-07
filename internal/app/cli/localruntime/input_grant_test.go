package localruntime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeclaredInputGrantUsesOwnedOfflineOperator(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	_, state, err := controller.Attach(t.Context())
	require.NoError(t, err)
	runner.responses = map[string][]byte{"admin access stage-grant": inputGrantReceipt(t, state, nil)}
	request := inputGrantRequest()
	require.NoError(t, controller.StageDeclaredInputUploadGrant(t.Context(), state, request))
	want := "run --rm --no-deps leapview admin access stage-grant --project lvproject_test --id fixture-upload --principal authenticated-owner --resource connection:sample --kind connection --action connection.upload --expected-revision 3 --operation-id fixture-operation --apply"
	require.Equal(t, 1, countCommands(runner.commands, want))
	command := runner.commands[commandIndex(runner.commands, want)]
	require.Equal(t, []string{"--host", controller.endpoint.Host()}, command[:2])
	require.Contains(t, strings.Join(command, " "), "--project-name "+state.Runtime.ComposeProject)
}

func TestDeclaredInputGrantRejectsUnverifiedRuntimeBeforeMutation(t *testing.T) {
	for _, name := range []string{"checkout", "runtime", "endpoint", "project", "target", "environment", "session", "reset", "ownership", "endpoint changed", "no attachment", "invalid principal", "invalid connection", "invalid revision"} {
		t.Run(name, func(t *testing.T) {
			controller, runner, _ := startedLifecycleController(t)
			attachment, state, err := controller.Attach(t.Context())
			require.NoError(t, err)
			request := inputGrantRequest()
			switch name {
			case "checkout":
				state.Checkout.ID = "different"
			case "runtime":
				state.Runtime.OwnerID = "different"
			case "endpoint":
				state.Endpoint.ServerID = "different"
			case "project":
				state.Authority.ProjectUID = "different"
			case "target":
				state.Authority.InstanceID = "different"
			case "environment":
				state.Authority.Environment = "prod"
			case "session":
				state.Session.SessionID = "different"
			case "reset":
				state.Reset = &resetState{Stage: resetStagePlanned}
			case "ownership":
				runner.responses = map[string][]byte{"container ls": []byte("foreign-container\n"), "container inspect": []byte(`{"io.leapview.local-runtime":"false"}`)}
			case "endpoint changed":
				controller.endpoint.(*fakeEndpoint).verifyErr = errors.New("daemon identity changed")
			case "no attachment":
				_, err = controller.Detach(t.Context(), attachment)
				require.NoError(t, err)
			case "invalid principal":
				request.PrincipalID = ""
			case "invalid connection":
				request.ConnectionID = "invalid connection"
			case "invalid revision":
				request.ExpectedRevision = 0
			}
			require.Error(t, controller.StageDeclaredInputUploadGrant(t.Context(), state, request))
			require.Zero(t, countCommands(runner.commands, "admin access stage-grant"))
		})
	}
}

func TestDeclaredInputGrantRejectsWrongOperatorEvidence(t *testing.T) {
	for _, name := range []string{"targetId", "projectId", "environment", "policyRevision", "policyDigest", "applied", "requiresPublication", "command failure"} {
		t.Run(name, func(t *testing.T) {
			controller, runner, _ := startedLifecycleController(t)
			_, state, err := controller.Attach(t.Context())
			require.NoError(t, err)
			if name == "command failure" {
				runner.failOnce = "admin access stage-grant"
			} else {
				runner.responses = map[string][]byte{"admin access stage-grant": inputGrantReceipt(t, state, func(body map[string]any) {
					switch name {
					case "policyRevision":
						body[name] = 3
					case "applied", "requiresPublication":
						body[name] = false
					default:
						body[name] = "other"
					}
				})}
			}
			require.Error(t, controller.StageDeclaredInputUploadGrant(t.Context(), state, inputGrantRequest()))
		})
	}
}

func inputGrantRequest() DeclaredInputUploadGrantRequest {
	return DeclaredInputUploadGrantRequest{PrincipalID: "authenticated-owner", ConnectionID: "connection:sample", GrantID: "fixture-upload", OperationID: "fixture-operation", ExpectedRevision: 3}
}

func inputGrantReceipt(t *testing.T, state State, change func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{"targetId": state.Authority.InstanceID, "projectId": state.Authority.ProjectUID, "environment": "dev", "policyRevision": 4, "policyDigest": "sha256:" + strings.Repeat("a", 64), "applied": true, "requiresPublication": true}
	if change != nil {
		change(body)
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return encoded
}
