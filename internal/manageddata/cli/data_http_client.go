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
	"strconv"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	manageddataapi "github.com/flidai/leapview/internal/manageddata/api"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

type managedDataCLIClient struct {
	http   *http.Client
	target string
	token  string
}

func newManagedDataCLIClient(client *http.Client, target, token string) *managedDataCLIClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &managedDataCLIClient{http: client, target: strings.TrimRight(target, "/"), token: token}
}

func (client *managedDataCLIClient) createUploadSession(ctx context.Context, project, connection, key string, body manageddataapi.ManagedDataUploadSessionCreateRequest) (manageddataapi.ManagedDataUploadSessionResponse, error) {
	generatedBody, err := managedDataConvert[manageddatagen.GenSchemaManagedDataUploadSessionCreateRequest](body)
	if err != nil {
		return manageddataapi.ManagedDataUploadSessionResponse{}, fmt.Errorf("encode create managed-data upload session request: %w", err)
	}
	response, err := client.generated().CreateManagedDataUploadSession(ctx, manageddatagen.GenCreateManagedDataUploadSessionClientRequest{
		Project: project, Connection: connection,
		Headers: manageddatagen.GenCreateManagedDataUploadSessionClientHeaders{IdempotencyKey: key},
		Body:    generatedBody,
	})
	if err != nil {
		var failure manageddatagen.GenCreateManagedDataUploadSessionFailure
		if errors.As(err, &failure) {
			return manageddataapi.ManagedDataUploadSessionResponse{}, createUploadSessionFailure(failure)
		}
		return manageddataapi.ManagedDataUploadSessionResponse{}, err
	}
	return managedDataConvert[manageddataapi.ManagedDataUploadSessionResponse](response.Body)
}

func (client *managedDataCLIClient) finalizeUploadSession(ctx context.Context, project, connection, uploadID, key string) (manageddataapi.ManagedDataUploadSessionResponse, error) {
	response, err := client.generated().FinalizeManagedDataUploadSession(ctx, manageddatagen.GenFinalizeManagedDataUploadSessionClientRequest{
		Project: project, Connection: connection, UploadSession: uploadID,
		Headers: manageddatagen.GenFinalizeManagedDataUploadSessionClientHeaders{IdempotencyKey: key},
	})
	if err != nil {
		var failure manageddatagen.GenFinalizeManagedDataUploadSessionFailure
		if errors.As(err, &failure) {
			return manageddataapi.ManagedDataUploadSessionResponse{}, finalizeUploadSessionFailure(failure)
		}
		return manageddataapi.ManagedDataUploadSessionResponse{}, err
	}
	return managedDataConvert[manageddataapi.ManagedDataUploadSessionResponse](response.Body)
}

func (client *managedDataCLIClient) getUploadSession(ctx context.Context, project, connection, uploadID string) (manageddataapi.ManagedDataUploadSessionResponse, error) {
	var response manageddataapi.ManagedDataUploadSessionResponse
	err := client.json(ctx, http.MethodGet, "getManagedDataUploadSession", managedDataUploadPath(project, connection, uploadID), nil, "", nil, &response)
	return response, err
}

func (client *managedDataCLIClient) abortUploadSession(ctx context.Context, project, connection, uploadID, key string) {
	_, err := client.generated().CancelManagedDataUploadSession(ctx, manageddatagen.GenCancelManagedDataUploadSessionClientRequest{
		Project: project, Connection: connection, UploadSession: uploadID,
		Headers: manageddatagen.GenCancelManagedDataUploadSessionClientHeaders{IdempotencyKey: key},
	})
	if err != nil {
		var failure manageddatagen.GenCancelManagedDataUploadSessionFailure
		if errors.As(err, &failure) {
			_ = cancelUploadSessionFailure(failure)
		}
	}
}

func (client *managedDataCLIClient) createMultipart(ctx context.Context, project, connection, uploadID, key, logicalPath string) (manageddataapi.ManagedDataS3MultipartUploadResponse, error) {
	response, err := client.generated().CreateManagedDataS3MultipartUpload(ctx, manageddatagen.GenCreateManagedDataS3MultipartUploadClientRequest{
		Project: project, Connection: connection, UploadSession: uploadID,
		Headers: manageddatagen.GenCreateManagedDataS3MultipartUploadClientHeaders{IdempotencyKey: key},
		Body:    manageddatagen.GenSchemaManagedDataS3MultipartCreateRequest{Path: logicalPath},
	})
	if err != nil {
		var failure manageddatagen.GenCreateManagedDataS3MultipartUploadFailure
		if errors.As(err, &failure) {
			return manageddataapi.ManagedDataS3MultipartUploadResponse{}, createMultipartFailure(failure)
		}
		return manageddataapi.ManagedDataS3MultipartUploadResponse{}, err
	}
	return managedDataConvert[manageddataapi.ManagedDataS3MultipartUploadResponse](response.Body)
}

func (client *managedDataCLIClient) signMultipartPart(ctx context.Context, project, connection, uploadID, multipartID string, partNumber int32, body manageddataapi.ManagedDataS3MultipartSignPartRequest) (manageddataapi.ManagedDataS3MultipartSignedPartResponse, error) {
	params := managedDataMultipartPath(project, connection, uploadID, multipartID)
	params["partNumber"] = strconv.FormatInt(int64(partNumber), 10)
	var response manageddataapi.ManagedDataS3MultipartSignedPartResponse
	err := client.json(ctx, http.MethodPost, "signManagedDataS3MultipartPart", params, nil, "", body, &response)
	return response, err
}

func (client *managedDataCLIClient) completeMultipart(ctx context.Context, project, connection, uploadID, multipartID, key string, body manageddataapi.ManagedDataS3MultipartCompleteRequest) (manageddataapi.ManagedDataS3MultipartUploadResponse, error) {
	generatedBody, err := managedDataConvert[manageddatagen.GenSchemaManagedDataS3MultipartCompleteRequest](body)
	if err != nil {
		return manageddataapi.ManagedDataS3MultipartUploadResponse{}, fmt.Errorf("encode complete managed-data multipart upload request: %w", err)
	}
	response, err := client.generated().CompleteManagedDataS3MultipartUpload(ctx, manageddatagen.GenCompleteManagedDataS3MultipartUploadClientRequest{
		Project: project, Connection: connection, UploadSession: uploadID, MultipartUpload: multipartID,
		Headers: manageddatagen.GenCompleteManagedDataS3MultipartUploadClientHeaders{IdempotencyKey: key},
		Body:    generatedBody,
	})
	if err != nil {
		var failure manageddatagen.GenCompleteManagedDataS3MultipartUploadFailure
		if errors.As(err, &failure) {
			return manageddataapi.ManagedDataS3MultipartUploadResponse{}, completeMultipartFailure(failure)
		}
		return manageddataapi.ManagedDataS3MultipartUploadResponse{}, err
	}
	return managedDataConvert[manageddataapi.ManagedDataS3MultipartUploadResponse](response.Body)
}

func (client *managedDataCLIClient) abortMultipart(ctx context.Context, project, connection, uploadID, multipartID, key string) {
	_, err := client.generated().AbortManagedDataS3MultipartUpload(ctx, manageddatagen.GenAbortManagedDataS3MultipartUploadClientRequest{
		Project: project, Connection: connection, UploadSession: uploadID, MultipartUpload: multipartID,
		Headers: manageddatagen.GenAbortManagedDataS3MultipartUploadClientHeaders{IdempotencyKey: key},
	})
	if err != nil {
		var failure manageddatagen.GenAbortManagedDataS3MultipartUploadFailure
		if errors.As(err, &failure) {
			_ = abortMultipartFailure(failure)
		}
	}
}

func (client *managedDataCLIClient) listRevisions(ctx context.Context, project, connection string, query url.Values) (manageddataapi.ManagedDataRevisionListResponse, error) {
	var response manageddataapi.ManagedDataRevisionListResponse
	err := client.json(ctx, http.MethodGet, "listManagedDataRevisions", map[string]string{"project": project, "connection": connection}, query, "", nil, &response)
	return response, err
}

func (client *managedDataCLIClient) currentRevision(ctx context.Context, project, connection, _ string) (manageddataapi.ManagedDataActiveRevisionResponse, error) {
	var response manageddataapi.ManagedDataActiveRevisionResponse
	err := client.json(ctx, http.MethodGet, "getActiveManagedDataRevision", map[string]string{"project": project, "connection": connection}, nil, "", nil, &response)
	return response, err
}

func (client *managedDataCLIClient) generated() *manageddatagen.GenClient {
	return manageddatagen.NewGenClient(cliapi.HTTPTransport{
		Target: client.target,
		Token:  client.token,
		Client: client.http,
		PrepareRequest: func(request *http.Request) {
			request.Header.Set("X-LeapView-Invocation-Surface", "cli")
		},
	})
}

func managedDataConvert[T any](value any) (T, error) {
	var converted T
	encoded, err := json.Marshal(value)
	if err != nil {
		return converted, err
	}
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return converted, err
	}
	return converted, nil
}

func managedDataProblemError(operation, variant string) func(apigenclient.ProblemDetails) error {
	return func(problem apigenclient.ProblemDetails) error {
		label := operation
		if variant != "" {
			label += " " + variant
		}
		detail := strings.TrimSpace(problem.Detail)
		if detail == "" {
			detail = strings.TrimSpace(problem.Title)
		}
		if detail == "" {
			detail = "request failed"
		}
		if code := strings.TrimSpace(problem.Code); code != "" {
			return fmt.Errorf("%s: %s (%s)", label, detail, code)
		}
		return fmt.Errorf("%s: %s", label, detail)
	}
}

func createUploadSessionFailure(failure manageddatagen.GenCreateManagedDataUploadSessionFailure) error {
	return manageddatagen.MatchGenCreateManagedDataUploadSessionFailure(
		failure,
		managedDataProblemError("create managed-data upload session", "backend unavailable"),
		managedDataProblemError("create managed-data upload session", "conflict"),
		managedDataProblemError("create managed-data upload session", "expired"),
		managedDataProblemError("create managed-data upload session", "incomplete"),
		managedDataProblemError("create managed-data upload session", "integrity failure"),
		managedDataProblemError("create managed-data upload session", "internal failure"),
		managedDataProblemError("create managed-data upload session", "invalid request"),
		managedDataProblemError("create managed-data upload session", "not found"),
		managedDataProblemError("create managed-data upload session", "too large"),
		managedDataProblemError("create managed-data upload session", "unavailable"),
	)
}

func finalizeUploadSessionFailure(failure manageddatagen.GenFinalizeManagedDataUploadSessionFailure) error {
	return manageddatagen.MatchGenFinalizeManagedDataUploadSessionFailure(
		failure,
		managedDataProblemError("finalize managed-data upload session", "backend unavailable"),
		managedDataProblemError("finalize managed-data upload session", "conflict"),
		managedDataProblemError("finalize managed-data upload session", "expired"),
		managedDataProblemError("finalize managed-data upload session", "incomplete"),
		managedDataProblemError("finalize managed-data upload session", "integrity failure"),
		managedDataProblemError("finalize managed-data upload session", "internal failure"),
		managedDataProblemError("finalize managed-data upload session", "invalid request"),
		managedDataProblemError("finalize managed-data upload session", "not found"),
		managedDataProblemError("finalize managed-data upload session", "too large"),
		managedDataProblemError("finalize managed-data upload session", "unavailable"),
	)
}

func cancelUploadSessionFailure(failure manageddatagen.GenCancelManagedDataUploadSessionFailure) error {
	return manageddatagen.MatchGenCancelManagedDataUploadSessionFailure(
		failure,
		managedDataProblemError("cancel managed-data upload session", "backend unavailable"),
		managedDataProblemError("cancel managed-data upload session", "conflict"),
		managedDataProblemError("cancel managed-data upload session", "expired"),
		managedDataProblemError("cancel managed-data upload session", "incomplete"),
		managedDataProblemError("cancel managed-data upload session", "integrity failure"),
		managedDataProblemError("cancel managed-data upload session", "internal failure"),
		managedDataProblemError("cancel managed-data upload session", "invalid request"),
		managedDataProblemError("cancel managed-data upload session", "not found"),
		managedDataProblemError("cancel managed-data upload session", "too large"),
		managedDataProblemError("cancel managed-data upload session", "unavailable"),
	)
}

func createMultipartFailure(failure manageddatagen.GenCreateManagedDataS3MultipartUploadFailure) error {
	return manageddatagen.MatchGenCreateManagedDataS3MultipartUploadFailure(
		failure,
		managedDataProblemError("create managed-data multipart upload", "backend unavailable"),
		managedDataProblemError("create managed-data multipart upload", "conflict"),
		managedDataProblemError("create managed-data multipart upload", "expired"),
		managedDataProblemError("create managed-data multipart upload", "incomplete"),
		managedDataProblemError("create managed-data multipart upload", "integrity failure"),
		managedDataProblemError("create managed-data multipart upload", "internal failure"),
		managedDataProblemError("create managed-data multipart upload", "invalid request"),
		managedDataProblemError("create managed-data multipart upload", "not found"),
		managedDataProblemError("create managed-data multipart upload", "unavailable"),
	)
}

func completeMultipartFailure(failure manageddatagen.GenCompleteManagedDataS3MultipartUploadFailure) error {
	return manageddatagen.MatchGenCompleteManagedDataS3MultipartUploadFailure(
		failure,
		managedDataProblemError("complete managed-data multipart upload", "backend unavailable"),
		managedDataProblemError("complete managed-data multipart upload", "conflict"),
		managedDataProblemError("complete managed-data multipart upload", "expired"),
		managedDataProblemError("complete managed-data multipart upload", "incomplete"),
		managedDataProblemError("complete managed-data multipart upload", "integrity failure"),
		managedDataProblemError("complete managed-data multipart upload", "internal failure"),
		managedDataProblemError("complete managed-data multipart upload", "invalid request"),
		managedDataProblemError("complete managed-data multipart upload", "not found"),
		managedDataProblemError("complete managed-data multipart upload", "too large"),
		managedDataProblemError("complete managed-data multipart upload", "unavailable"),
	)
}

func abortMultipartFailure(failure manageddatagen.GenAbortManagedDataS3MultipartUploadFailure) error {
	return manageddatagen.MatchGenAbortManagedDataS3MultipartUploadFailure(
		failure,
		managedDataProblemError("abort managed-data multipart upload", "backend unavailable"),
		managedDataProblemError("abort managed-data multipart upload", "conflict"),
		managedDataProblemError("abort managed-data multipart upload", "expired"),
		managedDataProblemError("abort managed-data multipart upload", "incomplete"),
		managedDataProblemError("abort managed-data multipart upload", "integrity failure"),
		managedDataProblemError("abort managed-data multipart upload", "internal failure"),
		managedDataProblemError("abort managed-data multipart upload", "invalid request"),
		managedDataProblemError("abort managed-data multipart upload", "not found"),
		managedDataProblemError("abort managed-data multipart upload", "unavailable"),
	)
}

func (client *managedDataCLIClient) json(ctx context.Context, method, operation string, pathParams map[string]string, query url.Values, idempotencyKey string, body, out any) error {
	endpoint, err := managedDataOperationURL(client.target, operation, pathParams, query)
	if err != nil {
		return fmt.Errorf("build managed-data request: %w", err)
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode managed-data request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build managed-data request: %w", err)
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

func managedDataOperationURL(target, operation string, pathParams map[string]string, query url.Values) (string, error) {
	contract, ok := manageddatagen.GetAPIGenOperationContract(operation)
	if !ok {
		return "", fmt.Errorf("unknown Managed Data API operation %q", operation)
	}
	path := contract.Path
	for name, value := range pathParams {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	if strings.Contains(path, "{") {
		return "", fmt.Errorf("unresolved API path parameter in %q", path)
	}
	endpoint, err := url.Parse(strings.TrimRight(target, "/") + path)
	if err != nil {
		return "", err
	}
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}
	return endpoint.String(), nil
}

func managedDataUploadPath(project, connection, uploadID string) map[string]string {
	return map[string]string{"project": project, "connection": connection, "uploadSession": uploadID}
}

func managedDataMultipartPath(project, connection, uploadID, multipartID string) map[string]string {
	params := managedDataUploadPath(project, connection, uploadID)
	params["multipartUpload"] = multipartID
	return params
}
