package cobra

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildRequestBodyMultipartValidatesParts(t *testing.T) {
	t.Helper()
	spec := multipartCommandSpec()

	cmd := newGeneratedLeafCommand(spec, NewClient("", "", ""), RuntimeOptions{})
	body, err := buildRequestBody(cmd, spec, nil)
	require.ErrorContains(t, err, `required multipart part "metadata" is missing`)
	require.Nil(t, body)

	cmd = newGeneratedLeafCommand(spec, NewClient("", "", ""), RuntimeOptions{})
	require.NoError(t, cmd.Flags().Set("part", `unknown=value`))
	_, err = buildRequestBody(cmd, spec, nil)
	require.ErrorContains(t, err, `unknown multipart part "unknown"`)

	cmd = newGeneratedLeafCommand(spec, NewClient("", "", ""), RuntimeOptions{})
	require.NoError(t, cmd.Flags().Set("part", `metadata={"name":"one"}`))
	require.NoError(t, cmd.Flags().Set("part", `metadata={"name":"two"}`))
	require.NoError(t, cmd.Flags().Set("part", `artifact=@`+writeTempFile(t, "artifact.bin", "payload")))
	_, err = buildRequestBody(cmd, spec, nil)
	require.ErrorContains(t, err, `duplicate multipart part "metadata"`)
}

func TestBuildRequestBodyMultipartParsesInputs(t *testing.T) {
	t.Helper()
	spec := multipartCommandSpec()
	artifactPath := writeTempFile(t, "artifact.bin", "payload")
	notePath := writeTempFile(t, "note.txt", "hello from file")

	cmd := newGeneratedLeafCommand(spec, NewClient("", "", ""), RuntimeOptions{})
	require.NoError(t, cmd.Flags().Set("part", `metadata={"name":"one"}`))
	require.NoError(t, cmd.Flags().Set("part", `artifact=@`+artifactPath))
	require.NoError(t, cmd.Flags().Set("part", `tag=alpha`))
	require.NoError(t, cmd.Flags().Set("part", `tag=beta`))
	require.NoError(t, cmd.Flags().Set("part", `note=@`+notePath))

	body, err := buildRequestBody(cmd, spec, nil)
	require.NoError(t, err)
	multipartBody := body.(MultipartBody)
	require.Equal(t, "multipart/form-data", multipartBody.ContentType)
	require.Len(t, multipartBody.Parts, 5)
	require.Equal(t, "metadata", multipartBody.Parts[0].Name)
	require.JSONEq(t, `{"name":"one"}`, string(multipartBody.Parts[0].Data))
	require.Equal(t, "artifact", multipartBody.Parts[1].Name)
	require.Equal(t, []byte("payload"), multipartBody.Parts[1].Data)
	require.NotNil(t, multipartBody.Parts[1].Filename)
	require.Equal(t, "artifact.bin", *multipartBody.Parts[1].Filename)
	require.Equal(t, "tag", multipartBody.Parts[2].Name)
	require.Equal(t, []byte("alpha"), multipartBody.Parts[2].Data)
	require.Equal(t, "tag", multipartBody.Parts[3].Name)
	require.Equal(t, []byte("beta"), multipartBody.Parts[3].Data)
	require.Equal(t, "note", multipartBody.Parts[4].Name)
	require.Equal(t, []byte("hello from file"), multipartBody.Parts[4].Data)
}

func TestBuildRequestBodyMultipartRequiresFilesForBinaryParts(t *testing.T) {
	t.Helper()
	spec := multipartCommandSpec()
	cmd := newGeneratedLeafCommand(spec, NewClient("", "", ""), RuntimeOptions{})
	require.NoError(t, cmd.Flags().Set("part", `metadata={"name":"one"}`))
	require.NoError(t, cmd.Flags().Set("part", `artifact=payload`))

	_, err := buildRequestBody(cmd, spec, nil)
	require.ErrorContains(t, err, `multipart part "artifact" requires @file or - input`)
}

func TestClientDoMultipartEncodesParts(t *testing.T) {
	t.Helper()
	var gotContentType string
	var gotBody []byte
	client := NewClient("https://example.test", "", "")
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotContentType = req.Header.Get("Content-Type")
		var err error
		gotBody, err = io.ReadAll(req.Body)
		require.NoError(t, err)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	})}
	filename := "artifact.bin"
	body := MultipartBody{
		ContentType: "multipart/form-data",
		Parts: []MultipartPart{
			{Name: "metadata", ContentType: "application/json", Data: []byte(`{"name":"one"}`)},
			{Name: "artifact", ContentType: "application/octet-stream", Filename: &filename, Data: []byte("payload")},
		},
	}

	resp, err := client.Do(http.MethodPost, "/artifacts", url.Values{}, body, "multipart/form-data", "multipart")
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	require.True(t, strings.HasPrefix(gotContentType, "multipart/form-data; boundary="), gotContentType)
	reader := multipart.NewReader(bytes.NewReader(gotBody), strings.TrimPrefix(gotContentType, "multipart/form-data; boundary="))
	part, err := reader.NextPart()
	require.NoError(t, err)
	require.Equal(t, "metadata", part.FormName())
	require.Equal(t, "application/json", part.Header.Get("Content-Type"))
	payload, err := io.ReadAll(part)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"one"}`, string(payload))
	part, err = reader.NextPart()
	require.NoError(t, err)
	require.Equal(t, "artifact", part.FormName())
	require.Equal(t, "artifact.bin", part.FileName())
	require.Equal(t, "application/octet-stream", part.Header.Get("Content-Type"))
	payload, err = io.ReadAll(part)
	require.NoError(t, err)
	require.Equal(t, "payload", string(payload))
}

func TestClientDoWithHeadersAppliesAuthoredHeadersAndDefaults(t *testing.T) {
	t.Helper()
	var gotAccept string
	var gotContentType string
	client := NewClient("https://example.test", "", "")
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAccept = req.Header.Get("Accept")
		gotContentType = req.Header.Get("Content-Type")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	})}

	resp, err := client.DoWithHeaders(http.MethodPost, "/widgets", url.Values{}, http.Header{
		"Accept":       []string{"application/octet-stream"},
		"Content-Type": []string{"text/plain"},
	}, "hello", "application/json", "text")

	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, "application/octet-stream", gotAccept)
	require.Equal(t, "text/plain", gotContentType)
}

func TestRunGeneratedCommandSendsHeaderFlags(t *testing.T) {
	t.Helper()
	var gotAccept string
	client := NewClient("https://example.test", "", "")
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAccept = req.Header.Get("Accept")
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	spec := CommandSpec{
		OperationID: "downloadArtifact",
		Method:      http.MethodGet,
		Path:        "/artifacts/{id}",
		Command:     []string{"artifact", "download"},
		Args:        []ArgBinding{{Name: "id", Source: "path"}},
		Parameters: []Param{
			{Name: "id", In: "path", Type: "string", Required: true},
			{Name: "accept", In: "header", Type: "string", Required: true, Enum: []string{"application/json", "application/octet-stream"}},
		},
		Output: OutputSpec{Mode: "empty"},
	}
	cmd := newGeneratedLeafCommand(spec, client, RuntimeOptions{})
	require.NoError(t, cmd.Flags().Set("accept", "application/octet-stream"))

	err := runGeneratedCommand(cmd, client, spec, []string{"artifact-1"}, RuntimeOptions{})
	require.NoError(t, err)
	require.Equal(t, "application/octet-stream", gotAccept)
}

func multipartCommandSpec() CommandSpec {
	return CommandSpec{
		OperationID: "uploadArtifact",
		Method:      http.MethodPost,
		Path:        "/artifacts",
		Command:     []string{"artifact", "upload"},
		RequestBody: &RequestBodySpec{
			Required:    true,
			ContentType: "multipart/form-data",
			BodyKind:    "multipart",
			InputMode:   "multipart",
			Parts: []MultipartPartSpec{
				{Name: "metadata", WireName: "metadata", Required: true, ContentType: "application/json", BodyKind: "json", SchemaType: "object"},
				{Name: "artifact", WireName: "artifact", Required: true, ContentType: "application/octet-stream", BodyKind: "file", Filename: true, SchemaType: "string"},
				{Name: "tag", WireName: "tag", Repeated: true, ContentType: "text/plain", BodyKind: "text", SchemaType: "string"},
				{Name: "note", WireName: "note", ContentType: "text/plain", BodyKind: "text", SchemaType: "string"},
			},
		},
	}
}

func writeTempFile(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
