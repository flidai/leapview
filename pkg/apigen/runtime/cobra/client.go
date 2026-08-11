package cobra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client is the shared HTTP client used by the generated Cobra runtime.
type Client struct {
	BaseURL    string
	APIKey     string
	Token      string
	HTTPClient *http.Client
	Debug      bool
	TraceHTTP  bool
	LogFormat  string
	LogFile    string
}

// APIError is a structured API failure returned by CheckError.
type APIError struct {
	HTTPStatus int
	Code       int    `json:"code"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error (HTTP %d): %s", e.HTTPStatus, e.Message)
}

// NewClient constructs a runtime HTTP client with sane defaults.
func NewClient(baseURL, apiKey, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Do issues an authenticated HTTP request against the generated API surface.
func (c *Client) Do(method, path string, query url.Values, body any, contentType string, bodyKind string) (*http.Response, error) {
	return c.DoWithHeaders(method, path, query, nil, body, contentType, bodyKind)
}

// DoWithHeaders issues an authenticated HTTP request with generated header parameter values.
func (c *Client) DoWithHeaders(method, path string, query url.Values, headers http.Header, body any, contentType string, bodyKind string) (*http.Response, error) {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}

	reqURL := baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		switch bodyKind {
		case "text":
			bodyReader = strings.NewReader(fmt.Sprint(body))
		case "binary", "file":
			switch typed := body.(type) {
			case []byte:
				bodyReader = bytes.NewReader(typed)
			case string:
				bodyReader = strings.NewReader(typed)
			default:
				return nil, fmt.Errorf("unsupported %s request body type %T", bodyKind, body)
			}
		case "form_urlencoded":
			values, ok := body.(url.Values)
			if !ok {
				return nil, fmt.Errorf("form_urlencoded request body must be url.Values")
			}
			bodyReader = strings.NewReader(values.Encode())
		case "multipart":
			var multipartBody MultipartBody
			switch typed := body.(type) {
			case MultipartBody:
				multipartBody = typed
			case *MultipartBody:
				if typed == nil {
					return nil, fmt.Errorf("multipart request body must not be nil")
				}
				multipartBody = *typed
			default:
				return nil, fmt.Errorf("multipart request body must be MultipartBody")
			}
			payload, encodedContentType, err := encodeMultipartBody(multipartBody, contentType)
			if err != nil {
				return nil, err
			}
			bodyReader = payload
			contentType = encodedContentType
		default:
			payload, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(payload)
		}
	}

	req, err := http.NewRequestWithContext(context.Background(), method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if body != nil {
		if req.Header.Get("Content-Type") == "" {
			if strings.TrimSpace(contentType) == "" {
				contentType = "application/json"
			}
			req.Header.Set("Content-Type", contentType)
		}
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	} else if c.APIKey != "" {
		req.Header.Set("X-API-Key", c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	c.logRequest(req, resp, body)
	return resp, nil
}

func encodeMultipartBody(body MultipartBody, defaultContentType string) (io.Reader, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	contentType := body.ContentType
	if strings.TrimSpace(contentType) == "" {
		contentType = defaultContentType
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "multipart/form-data"
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		return nil, "", fmt.Errorf("multipart request content type must start with multipart/")
	}
	for _, part := range body.Parts {
		header := make(textproto.MIMEHeader)
		if strings.TrimSpace(part.ContentType) != "" {
			header.Set("Content-Type", part.ContentType)
		}
		if strings.EqualFold(contentType, "multipart/form-data") || strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data;") {
			params := map[string]string{"name": part.Name}
			if part.Filename != nil && strings.TrimSpace(*part.Filename) != "" {
				params["filename"] = *part.Filename
			}
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", params))
		} else if strings.TrimSpace(part.Name) != "" || part.Filename != nil {
			params := map[string]string{}
			if strings.TrimSpace(part.Name) != "" {
				params["name"] = part.Name
			}
			if part.Filename != nil && strings.TrimSpace(*part.Filename) != "" {
				params["filename"] = *part.Filename
			}
			header.Set("Content-Disposition", mime.FormatMediaType("attachment", params))
		}
		writerPart, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", fmt.Errorf("create multipart part %q: %w", part.Name, err)
		}
		if _, err := writerPart.Write(part.Data); err != nil {
			return nil, "", fmt.Errorf("write multipart part %q: %w", part.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart body: %w", err)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("parse multipart content type: %w", err)
	}
	return &buf, mediaType + "; boundary=" + writer.Boundary(), nil
}

func (c *Client) logRequest(req *http.Request, resp *http.Response, body any) {
	if !c.Debug && !c.TraceHTTP {
		return
	}

	writer := io.Writer(os.Stderr)
	if strings.TrimSpace(c.LogFile) != "" {
		f, err := os.OpenFile(c.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			writer = f
			defer func() {
				_ = f.Close()
			}()
		}
	}

	if strings.EqualFold(strings.TrimSpace(c.LogFormat), "json") {
		entry := map[string]any{
			"method":      req.Method,
			"url":         req.URL.String(),
			"status_code": resp.StatusCode,
		}
		if c.TraceHTTP && body != nil {
			entry["request_body"] = body
		}
		_ = json.NewEncoder(writer).Encode(entry)
		return
	}

	_, _ = fmt.Fprintf(writer, "[quack] %s %s -> %d\n", req.Method, req.URL.String(), resp.StatusCode)
	if c.TraceHTTP && body != nil {
		payload, _ := json.Marshal(body)
		if len(payload) > 0 {
			_, _ = fmt.Fprintf(writer, "[quack] body %s\n", string(payload))
		}
	}
}

// CheckError returns a structured error for non-2xx responses.
func CheckError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, err := ReadBody(resp)
	if err != nil {
		return fmt.Errorf("read error response: %w", err)
	}

	apiErr := &APIError{HTTPStatus: resp.StatusCode}
	if len(body) > 0 {
		if err := json.Unmarshal(body, apiErr); err == nil && strings.TrimSpace(apiErr.Message) != "" {
			if apiErr.Code == 0 {
				apiErr.Code = resp.StatusCode
			}
			return apiErr
		}
		apiErr.Message = string(body)
	}

	return apiErr
}

// ReadBody reads and closes an HTTP response body.
func ReadBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	return body, nil
}
