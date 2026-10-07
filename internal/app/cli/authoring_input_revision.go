package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

func syncDeclaredDevelopmentInput(ctx context.Context, request manageddatacli.SyncRequest) error {
	endpoint, err := cliapi.RequestURL(request.Target, "/api/v1/projects/{project}/connections/{connection}/upload-sessions", map[string]string{"project": request.ProjectID, "connection": request.ConnectionID}, nil)
	if err != nil {
		return err
	}
	client := request.HTTPClient
	if client == nil {
		client = defaultCLIHTTPClient
	}
	copyClient := *client
	transport := &developmentInputUploadTransport{base: client.Transport, endpoint: endpoint}
	if transport.base == nil {
		transport.base = http.DefaultTransport
	}
	copyClient.Transport = transport
	request.HTTPClient = &copyClient
	syncErr := manageddatacli.RunSync(ctx, request)
	if syncErr == nil {
		return nil
	}
	if !transport.denied.Load() {
		return syncErr
	}
	// A retained generation cannot gain a newly staged grant. An unchanged
	// fixture can still publish the successor policy using its authenticated,
	// available immutable revision; no local upload receipt is authority.
	if err := verifyDeclaredDevelopmentRevision(ctx, request); err != nil {
		return fmt.Errorf("%w (retained revision verification failed: %v; if this runtime predates declared-input upload grants, restore the previously staged fixture and run leapview dev once before reapplying the data edit)", syncErr, err)
	}
	out := request.Out
	if out == nil {
		out = io.Discard
	}
	_, err = fmt.Fprintf(out, "staged %s (retained revision verified)\n", request.Plan.Manifest.RevisionID())
	return err
}

// The server's native authorization boundary can return a plain-text 403.
// Capture the actual first upload-create response, never infer authority from
// an error string or recover a later transfer, integrity, or finalization error.
type developmentInputUploadTransport struct {
	base      http.RoundTripper
	endpoint  string
	attempted atomic.Bool
	denied    atomic.Bool
}

func (transport *developmentInputUploadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	first := !transport.attempted.Swap(true)
	response, err := transport.base.RoundTrip(request)
	if first && err == nil && response != nil && request.Method == http.MethodPost && request.URL.String() == transport.endpoint && response.StatusCode == http.StatusForbidden {
		transport.denied.Store(true)
	}
	return response, err
}

func verifyDeclaredDevelopmentRevision(ctx context.Context, request manageddatacli.SyncRequest) error {
	if ctx == nil || request.ProjectID == "" || request.Token == "" || request.ConnectionID == "" ||
		request.ConnectionID != request.Plan.Connection || request.Connection != request.Plan.ConnectionName {
		return errors.New("retained revision target does not match the validated development input")
	}
	expected, err := request.Plan.Manifest.CanonicalJSON()
	if err != nil || len(request.Plan.Manifest.Files) == 0 {
		return errors.New("retained revision requires a valid nonempty manifest")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := manageddatagen.NewGenClient(capabilityAPITransport{target: request.Target, token: request.Token, client: request.HTTPClient})
	response, err := client.GetManagedDataRevision(ctx, manageddatagen.GenGetManagedDataRevisionClientRequest{
		Project: request.ProjectID, Connection: request.ConnectionID, Revision: request.Plan.Manifest.RevisionID(),
	})
	if err != nil {
		return err
	}
	body := response.Body
	if body.Id != request.Plan.Manifest.RevisionID() || body.Status != "available" || body.UploadSessionId == "" || int(body.FileCount) != len(request.Plan.Manifest.Files) {
		return errors.New("server revision identity or availability does not match the declared input")
	}
	manifest := manageddata.Manifest{Files: make([]manageddata.File, len(body.Manifest.Files))}
	var size int64
	for index, file := range body.Manifest.Files {
		manifest.Files[index] = manageddata.File{Path: file.Path, Size: file.Size, SHA256: file.Sha256}
	}
	actual, err := manifest.CanonicalJSON()
	if err != nil || !bytes.Equal(expected, actual) {
		return errors.New("server revision manifest does not match the declared input")
	}
	for _, file := range request.Plan.Manifest.Files {
		size += file.Size
	}
	if body.Size != size {
		return errors.New("server revision size does not match the declared input")
	}
	return nil
}
