package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

type generatedFailureTransport struct {
	err      error
	response any
}

func (transport generatedFailureTransport) DoAPIGen(
	_ context.Context,
	_ apigenclient.Request,
	out any,
) (apigenclient.Response, error) {
	if transport.err != nil {
		return apigenclient.Response{}, transport.err
	}
	encoded, err := json.Marshal(transport.response)
	if err != nil {
		return apigenclient.Response{}, err
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return apigenclient.Response{}, err
	}
	return apigenclient.Response{StatusCode: http.StatusOK, Headers: http.Header{}}, nil
}

func TestGeneratedFailureMappersHandleEveryCandidateCommandVariant(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		call   func(error) error
		mapErr func(error) error
	}{
		{name: "commit conflict", status: http.StatusConflict, code: "CANDIDATE_CONFLICT", call: generatedCommitFailure, mapErr: mapCommitProjectCandidateSynchronizationFailure},
		{name: "commit invalid", status: http.StatusUnprocessableEntity, code: "INVALID_CANDIDATE", call: generatedCommitFailure, mapErr: mapCommitProjectCandidateSynchronizationFailure},
		{name: "commit not found", status: http.StatusNotFound, code: "CANDIDATE_NOT_FOUND", call: generatedCommitFailure, mapErr: mapCommitProjectCandidateSynchronizationFailure},
		{name: "commit unavailable", status: http.StatusServiceUnavailable, code: "CANDIDATE_SERVICE_UNAVAILABLE", call: generatedCommitFailure, mapErr: mapCommitProjectCandidateSynchronizationFailure},
		{name: "upload audit", status: http.StatusServiceUnavailable, code: "AUDIT_UNAVAILABLE", call: generatedUploadFailure, mapErr: mapUploadProjectCandidateSourceBlobFailure},
		{name: "upload conflict", status: http.StatusConflict, code: "CANDIDATE_CONFLICT", call: generatedUploadFailure, mapErr: mapUploadProjectCandidateSourceBlobFailure},
		{name: "upload invalid candidate", status: http.StatusUnprocessableEntity, code: "INVALID_CANDIDATE", call: generatedUploadFailure, mapErr: mapUploadProjectCandidateSourceBlobFailure},
		{name: "upload unavailable", status: http.StatusServiceUnavailable, code: "CANDIDATE_SERVICE_UNAVAILABLE", call: generatedUploadFailure, mapErr: mapUploadProjectCandidateSourceBlobFailure},
		{name: "upload invalid blob", status: http.StatusUnprocessableEntity, code: "INVALID_CANDIDATE_SOURCE_BLOB", call: generatedUploadFailure, mapErr: mapUploadProjectCandidateSourceBlobFailure},
		{name: "publish approval credential", status: http.StatusUnauthorized, code: "APPROVAL_CREDENTIAL_REQUIRED", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish auth unavailable", status: http.StatusServiceUnavailable, code: "AUTHORIZATION_UNAVAILABLE", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish candidate conflict", status: http.StatusConflict, code: "CANDIDATE_CONFLICT", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish candidate invalid", status: http.StatusUnprocessableEntity, code: "INVALID_CANDIDATE", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish candidate not found", status: http.StatusNotFound, code: "CANDIDATE_NOT_FOUND", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish candidate unavailable", status: http.StatusServiceUnavailable, code: "CANDIDATE_SERVICE_UNAVAILABLE", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish conflict", status: http.StatusConflict, code: "DEPLOYMENT_CONFLICT", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish invalid", status: http.StatusUnprocessableEntity, code: "INVALID_DEPLOYMENT", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish not found", status: http.StatusNotFound, code: "DEPLOYMENT_NOT_FOUND", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish forbidden", status: http.StatusForbidden, code: "PUBLICATION_MANAGEMENT_REQUIRED", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish release incomplete", status: http.StatusConflict, code: "RELEASE_INCOMPLETE", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish release not ready", status: http.StatusConflict, code: "RELEASE_NOT_READY", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
		{name: "publish unavailable", status: http.StatusServiceUnavailable, code: "DEPLOYMENT_SERVICE_UNAVAILABLE", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call(generatedProblemErrorForTest(test.status, test.code))
			if err == nil {
				t.Fatal("generated client returned nil error")
			}
			mapped := test.mapErr(err)
			if mapped == nil || !strings.Contains(mapped.Error(), test.code) {
				t.Fatalf("mapped error = %v", mapped)
			}
			var declared deploymentgen.GenPublishProjectCandidateFailure
			if errors.As(mapped, &declared) {
				t.Fatalf("declared failure escaped mapping: %T", mapped)
			}
		})
	}
}

func TestGeneratedFailureMappersPreserveUnexpectedAndTransportErrors(t *testing.T) {
	sentinel := errors.New("network unavailable")
	unknown := generatedProblemErrorForTest(http.StatusConflict, "NEW_CANDIDATE_FAILURE")
	for _, test := range []struct {
		name   string
		call   func(error) error
		mapErr func(error) error
	}{
		{name: "commit unexpected", call: generatedCommitFailure, mapErr: mapCommitProjectCandidateSynchronizationFailure},
		{name: "upload unexpected", call: generatedUploadFailure, mapErr: mapUploadProjectCandidateSourceBlobFailure},
		{name: "publish unexpected", call: generatedPublishFailure, mapErr: mapPublishProjectCandidateFailure},
	} {
		t.Run(test.name+" problem", func(t *testing.T) {
			err := test.mapErr(test.call(unknown))
			var unexpected *apigenclient.UnexpectedProblemError
			if !errors.As(err, &unexpected) {
				t.Fatalf("error = %T %v", err, err)
			}
		})
		t.Run(test.name+" transport", func(t *testing.T) {
			err := test.mapErr(test.call(sentinel))
			if !errors.Is(err, sentinel) {
				t.Fatalf("error = %T %v", err, err)
			}
		})
	}
}

func generatedProblemErrorForTest(status int, code string) error {
	return &apigenclient.ProblemError{
		OperationID: "candidate-command",
		Response:    apigenclient.Response{StatusCode: status},
		Problem: apigenclient.ProblemDetails{
			Status: status, Code: code, Detail: "declared failure",
		},
	}
}

func generatedCommitFailure(err error) error {
	_, callErr := deploymentgen.NewGenClient(generatedFailureTransport{err: err}).CommitProjectCandidateSynchronization(
		context.Background(), deploymentgen.GenCommitProjectCandidateSynchronizationClientRequest{},
	)
	return callErr
}

func generatedUploadFailure(err error) error {
	_, callErr := deploymentgen.NewGenClient(generatedFailureTransport{err: err}).UploadProjectCandidateSourceBlob(
		context.Background(), deploymentgen.GenUploadProjectCandidateSourceBlobClientRequest{},
	)
	return callErr
}

func generatedPublishFailure(err error) error {
	_, callErr := deploymentgen.NewGenClient(generatedFailureTransport{err: err}).PublishProjectCandidate(
		context.Background(), deploymentgen.GenPublishProjectCandidateClientRequest{},
	)
	return callErr
}
