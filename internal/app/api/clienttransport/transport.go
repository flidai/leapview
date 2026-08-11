package clienttransport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
)

type Transport struct {
	Target           string
	Token            string
	Client           *http.Client
	MaxResponseBytes int64
	PrepareRequest   func(*http.Request)
}

func (transport Transport) DoAPIGen(
	ctx context.Context,
	request apigenclient.Request,
	out any,
) (apigenclient.Response, error) {
	endpoint, err := RequestURL(
		transport.Target,
		request.Path,
		request.PathParams,
		request.Query,
	)
	if err != nil {
		return apigenclient.Response{}, err
	}
	var body io.Reader
	if request.Body != nil {
		if strings.Contains(strings.ToLower(request.ContentType), "json") {
			encoded, err := json.Marshal(request.Body)
			if err != nil {
				return apigenclient.Response{}, fmt.Errorf(
					"encode %s request: %w",
					request.OperationID,
					err,
				)
			}
			body = bytes.NewReader(encoded)
		} else {
			switch value := request.Body.(type) {
			case []byte:
				body = bytes.NewReader(value)
			case string:
				body = strings.NewReader(value)
			default:
				return apigenclient.Response{}, fmt.Errorf(
					"encode %s request: unsupported %s body type %T",
					request.OperationID,
					request.ContentType,
					request.Body,
				)
			}
		}
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		request.Method,
		endpoint,
		body,
	)
	if err != nil {
		return apigenclient.Response{}, err
	}
	httpRequest.Header = request.Headers.Clone()
	if httpRequest.Header == nil {
		httpRequest.Header = make(http.Header)
	}
	if request.Accept != "" {
		httpRequest.Header.Set("Accept", request.Accept)
	}
	if request.ContentType != "" {
		httpRequest.Header.Set("Content-Type", request.ContentType)
	}
	if transport.Token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+transport.Token)
	}
	if transport.PrepareRequest != nil {
		transport.PrepareRequest(httpRequest)
	}
	client := transport.Client
	if client == nil {
		client = http.DefaultClient
	}
	httpResponse, err := client.Do(httpRequest)
	if err != nil {
		return apigenclient.Response{}, err
	}
	defer httpResponse.Body.Close()
	reader := io.Reader(httpResponse.Body)
	if transport.MaxResponseBytes > 0 {
		reader = io.LimitReader(reader, transport.MaxResponseBytes+1)
	}
	payload, readErr := io.ReadAll(reader)
	metadata := apigenclient.Response{
		StatusCode: httpResponse.StatusCode,
		Headers:    httpResponse.Header.Clone(),
		ContentType: httpResponse.Header.Get(
			"Content-Type",
		),
	}
	if readErr != nil {
		return metadata, readErr
	}
	if transport.MaxResponseBytes > 0 &&
		int64(len(payload)) > transport.MaxResponseBytes {
		return metadata, fmt.Errorf(
			"%s response exceeds %d bytes",
			request.OperationID,
			transport.MaxResponseBytes,
		)
	}
	if httpResponse.StatusCode >= http.StatusMultipleChoices {
		mediaType, _, _ := mime.ParseMediaType(metadata.ContentType)
		if mediaType == "application/problem+json" {
			var problem apigenclient.ProblemDetails
			if err := json.Unmarshal(payload, &problem); err == nil && strings.TrimSpace(problem.Code) != "" {
				return metadata, &apigenclient.ProblemError{
					OperationID: request.OperationID,
					Response:    metadata,
					Problem:     problem,
				}
			}
		}
		return metadata, fmt.Errorf(
			"%s %s: %s",
			request.Method,
			endpoint,
			strings.TrimSpace(string(payload)),
		)
	}
	if request.OperationID != "" &&
		!apiaggregate.APIGenOperationAllowsStatus(
			request.OperationID,
			httpResponse.StatusCode,
		) {
		return metadata, fmt.Errorf(
			"%s %s: unexpected success status %d for operation %s",
			request.Method,
			endpoint,
			httpResponse.StatusCode,
			request.OperationID,
		)
	}
	if out == nil || len(payload) == 0 {
		return metadata, nil
	}
	switch destination := out.(type) {
	case *[]byte:
		*destination = append((*destination)[:0], payload...)
	case *string:
		*destination = string(payload)
	default:
		if err := json.Unmarshal(payload, out); err != nil {
			return metadata, fmt.Errorf(
				"decode %s response: %w",
				request.OperationID,
				err,
			)
		}
	}
	return metadata, nil
}

func RequestURL(
	target string,
	path string,
	pathParams map[string]string,
	query url.Values,
) (string, error) {
	for name, value := range pathParams {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	if strings.Contains(path, "{") {
		return "", fmt.Errorf("unresolved API path parameter in %q", path)
	}
	endpoint := path
	if !strings.HasPrefix(path, "http://") &&
		!strings.HasPrefix(path, "https://") {
		endpoint = strings.TrimRight(target, "/") + path
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if len(query) != 0 {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}
