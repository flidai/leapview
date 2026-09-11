package module

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func TestNativeDeliveryRequestValidationParity(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	valid := NativeDeliveryBuildRequest{
		ProjectID: projectID, TargetID: "target", Environment: "prod",
		PlanID:      uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000101"),
		PrincipalID: "operator", IdempotencyKey: "request-key",
	}
	tests := []struct {
		name   string
		mutate func(*NativeDeliveryBuildRequest)
		want   string
	}{
		{
			name:   "project identity",
			mutate: func(request *NativeDeliveryBuildRequest) { request.ProjectID = "" },
			want:   fmt.Sprintf("%s: project identity:", deployment.ErrDeliveryInvalid),
		},
		{
			name:   "target required and canonical",
			mutate: func(request *NativeDeliveryBuildRequest) { request.TargetID = " target" },
			want:   fmt.Sprintf("%s: native delivery target is required and canonical", deployment.ErrDeliveryInvalid),
		},
		{
			name:   "target bounded",
			mutate: func(request *NativeDeliveryBuildRequest) { request.TargetID = strings.Repeat("x", 513) },
			want:   fmt.Sprintf("%s: native delivery target is invalid", deployment.ErrDeliveryInvalid),
		},
		{
			name:   "target control characters",
			mutate: func(request *NativeDeliveryBuildRequest) { request.TargetID = "target\n" },
			want:   fmt.Sprintf("%s: native delivery target is required and canonical", deployment.ErrDeliveryInvalid),
		},
		{
			name:   "environment mismatch",
			mutate: func(request *NativeDeliveryBuildRequest) { request.Environment = "stage" },
			want:   fmt.Sprintf("%s: environment does not match instance", deployment.ErrDeliveryInvalid),
		},
		{
			name:   "principal required and canonical",
			mutate: func(request *NativeDeliveryBuildRequest) { request.PrincipalID = "" },
			want:   fmt.Sprintf("%s: native delivery principal is required and canonical", deployment.ErrDeliveryInvalid),
		},
		{
			name:   "idempotency key bounded",
			mutate: func(request *NativeDeliveryBuildRequest) { request.IdempotencyKey = strings.Repeat("k", 513) },
			want:   fmt.Sprintf("%s: native delivery idempotency key is invalid", deployment.ErrDeliveryInvalid),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			build := valid
			test.mutate(&build)
			publish := NativeDeliveryPublishRequest{
				ProjectID: build.ProjectID, TargetID: build.TargetID, Environment: build.Environment,
				CandidateID: valid.PlanID, PrincipalID: build.PrincipalID, IdempotencyKey: build.IdempotencyKey,
			}
			rollback := NativeDeliveryRollbackRequest{
				ProjectID: build.ProjectID, TargetID: build.TargetID, Environment: build.Environment,
				GenerationID: valid.PlanID, PrincipalID: build.PrincipalID, IdempotencyKey: build.IdempotencyKey,
			}

			errs := []error{build.validate("prod"), publish.validate("prod"), rollback.validate("prod")}
			for _, validationErr := range errs {
				if validationErr == nil {
					t.Fatal("validation unexpectedly succeeded")
				}
				if validationErr.Error() != errs[0].Error() {
					t.Fatalf("validation error = %q, build error = %q", validationErr, errs[0])
				}
			}
			if !strings.HasPrefix(errs[0].Error(), test.want) {
				t.Fatalf("validation error = %q, want prefix %q", errs[0], test.want)
			}
		})
	}
}

func TestNativeDeliveryRequestValidationPreservesIdentityErrors(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		validate func() error
		want     string
	}{
		{
			name: "build plan",
			validate: func() error {
				return (NativeDeliveryBuildRequest{ProjectID: projectID, TargetID: "target", Environment: "prod", PrincipalID: "operator", IdempotencyKey: "request-key"}).validate("prod")
			},
			want: "plan identity must be a canonical UUID",
		},
		{
			name: "publish candidate",
			validate: func() error {
				return (NativeDeliveryPublishRequest{ProjectID: projectID, TargetID: "target", Environment: "prod", PrincipalID: "operator", IdempotencyKey: "request-key"}).validate("prod")
			},
			want: "candidate identity must be a canonical UUID",
		},
		{
			name: "rollback generation",
			validate: func() error {
				return (NativeDeliveryRollbackRequest{ProjectID: projectID, TargetID: "target", Environment: "prod", PrincipalID: "operator", IdempotencyKey: "request-key"}).validate("prod")
			},
			want: "generation identity must be a canonical UUID",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate()
			if err == nil || !errors.Is(err, deployment.ErrDeliveryInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want delivery-invalid error containing %q", err, test.want)
			}
		})
	}
}
