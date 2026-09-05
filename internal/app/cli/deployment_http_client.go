package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

type deploymentCLIClient struct {
	http   *http.Client
	target string
	token  string
}

func newDeploymentCLIClient(client *http.Client, target, token string) *deploymentCLIClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &deploymentCLIClient{http: client, target: strings.TrimRight(target, "/"), token: token}
}

func (client *deploymentCLIClient) instance(ctx context.Context) (apigenapi.InstanceResponse, error) {
	var response apigenapi.InstanceResponse
	err := client.json(ctx, http.MethodGet, "getInstance", nil, nil, "", nil, &response)
	return response, err
}

func (client *deploymentCLIClient) capabilities(ctx context.Context) (apigenapi.CapabilitiesResponse, error) {
	var response apigenapi.CapabilitiesResponse
	err := client.json(ctx, http.MethodGet, "getCapabilities", nil, nil, "", nil, &response)
	return response, err
}

func (client *deploymentCLIClient) createRelease(ctx context.Context, project, key string, body releasegen.ReleaseCreateRequest) (releasegen.ReleaseResponse, error) {
	generated := releasegen.NewGenClient(capabilityAPITransport{target: client.target, token: client.token, client: client.http})
	response, err := generated.CreateRelease(ctx, releasegen.GenCreateReleaseClientRequest{
		Project: project,
		Headers: releasegen.GenCreateReleaseClientHeaders{IdempotencyKey: key},
		Body:    body,
	})
	if err == nil {
		return response.Body, nil
	}
	var failure releasegen.GenCreateReleaseFailure
	if errors.As(err, &failure) {
		return releasegen.ReleaseResponse{}, createReleaseCLIError(failure)
	}
	return releasegen.ReleaseResponse{}, err
}

func createReleaseCLIError(failure releasegen.GenCreateReleaseFailure) error {
	return releasegen.MatchGenCreateReleaseFailure(
		failure,
		problemError("release creation conflict"),
		problemError("release request invalid"),
	)
}

func (client *deploymentCLIClient) finalizeRelease(ctx context.Context, project, releaseID, key string) (releasegen.ReleaseResponse, error) {
	generated := releasegen.NewGenClient(capabilityAPITransport{target: client.target, token: client.token, client: client.http})
	response, err := generated.FinalizeRelease(ctx, releasegen.GenFinalizeReleaseClientRequest{
		Project: project,
		Release: releaseID,
		Headers: releasegen.GenFinalizeReleaseClientHeaders{IdempotencyKey: key},
	})
	if err == nil {
		return response.Body, nil
	}
	var failure releasegen.GenFinalizeReleaseFailure
	if errors.As(err, &failure) {
		return releasegen.ReleaseResponse{}, finalizeReleaseCLIError(failure)
	}
	return releasegen.ReleaseResponse{}, err
}

func finalizeReleaseCLIError(failure releasegen.GenFinalizeReleaseFailure) error {
	return releasegen.MatchGenFinalizeReleaseFailure(
		failure,
		problemError("release finalization conflict"),
		problemError("release is immutable"),
		problemError("release artifacts are incomplete"),
		problemError("release not found"),
		problemError("release finalization queue unavailable"),
	)
}

func problemError(label string) func(apigenclient.ProblemDetails) error {
	return func(problem apigenclient.ProblemDetails) error {
		return fmt.Errorf("%s: %s (%s)", label, problem.Detail, problem.Code)
	}
}

func (client *deploymentCLIClient) getRelease(ctx context.Context, project, releaseID string) (releasegen.ReleaseResponse, error) {
	var response releasegen.ReleaseResponse
	err := client.json(ctx, http.MethodGet, "getRelease", map[string]string{"project": project, "release": releaseID}, nil, "", nil, &response)
	return response, err
}

func (client *deploymentCLIClient) json(ctx context.Context, method, operation string, pathParams map[string]string, query url.Values, idempotencyKey string, body, out any) error {
	endpoint, err := apiOperationURL(client.target, operation, pathParams, query)
	if err != nil {
		return fmt.Errorf("build deployment request: %w", err)
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode deployment request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build deployment request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.token != "" {
		request.Header.Set("Authorization", "Bearer "+client.token)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("operation %s could not reach the server", operation)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		contents, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(contents, &problem) == nil && strings.TrimSpace(problem.Detail) != "" {
			if strings.TrimSpace(problem.Code) != "" {
				return fmt.Errorf("operation %s failed with HTTP %d (%s): %s", operation, response.StatusCode, problem.Code, problem.Detail)
			}
			return fmt.Errorf("operation %s failed with HTTP %d: %s", operation, response.StatusCode, problem.Detail)
		}
		return fmt.Errorf("operation %s failed with HTTP %d", operation, response.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode operation %s response: %w", operation, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode operation %s response: trailing data", operation)
	}
	return nil
}
