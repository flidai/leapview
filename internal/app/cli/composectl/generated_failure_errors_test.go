package composectl

import (
	"context"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

type qualificationProblemTransport struct {
	problem *apigenclient.ProblemError
}

func (transport qualificationProblemTransport) DoAPIGen(_ context.Context, _ apigenclient.Request, _ any) (apigenclient.Response, error) {
	return transport.problem.Response, transport.problem
}

func TestQualificationGeneratedFailureMappings(t *testing.T) {
	tests := []struct {
		name string
		call func() error
		mapf func(error) error
		want string
	}{
		{
			name: "principal",
			call: func() error {
				_, err := accessgen.NewGenClient(qualificationProblemTransport{problem: &apigenclient.ProblemError{
					Response: apigenclient.Response{StatusCode: 409},
					Problem:  apigenclient.ProblemDetails{Code: "PRINCIPAL_ALREADY_EXISTS", Detail: "duplicate"},
				}}).CreatePrincipal(context.Background(), accessgen.GenCreatePrincipalClientRequest{})
				return err
			},
			mapf: mapQualificationCreatePrincipalFailure,
			want: "create qualification reviewer failed (PRINCIPAL_ALREADY_EXISTS)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("generated call error = nil")
			}
			mapped := test.mapf(err)
			if !strings.Contains(mapped.Error(), test.want) {
				t.Fatalf("mapped error = %v, want %q", mapped, test.want)
			}
		})
	}
}
