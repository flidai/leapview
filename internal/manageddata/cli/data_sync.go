package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddataapi "github.com/flidai/leapview/internal/manageddata/api"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/manageddata/qualificationbarrier"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/spf13/cobra"
)

const (
	dataTransferAttempts = 5
	dataTusChunkSize     = 16 << 20
)

// SyncRequest describes one Managed Data staging operation.
type SyncRequest struct {
	ProjectPath string
	ProjectID   string
	// Connection is the user-facing selector (usually an authored name).
	Connection string
	// ConnectionID is the canonical authored ID sent to the target API.
	ConnectionID string
	Root         string
	Target       string
	Token        string
	Plan         localplan.Result
	Out          io.Writer
	HTTPClient   *http.Client
	Format       string
}

type dataSyncRequest = SyncRequest

func dataSyncCommand(ctx context.Context, planner dataPlanner, dependencies Dependencies, opts *options) *cobra.Command {
	var projectPath string
	var connection string
	var from string
	format := "text"
	command := &cobra.Command{
		Use:   "sync",
		Short: "Stage a managed data revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(connection) == "" {
				return fmt.Errorf("connection is required")
			}
			if strings.TrimSpace(from) == "" {
				return fmt.Errorf("from is required")
			}
			if dependencies.Client == nil {
				return fmt.Errorf("Managed Data CLI API client is required")
			}
			credentials, err := dependencies.Client.Resolve(ctx, opts.remote.Credentials())
			if err != nil {
				return err
			}
			if dependencies.LoadProjectID == nil {
				return fmt.Errorf("Managed Data project identity loader is required")
			}
			projectID, err := dependencies.LoadProjectID(projectPath)
			if err != nil {
				return fmt.Errorf("load project: %w", err)
			}
			if strings.TrimSpace(projectID) == "" {
				return fmt.Errorf("project name is required")
			}
			plan, err := planner.Plan(ctx, localplan.Request{ProjectPath: projectPath, Connection: connection, From: from})
			if err != nil {
				return err
			}
			if _, err := dependencies.Client.Environment(ctx, credentials, opts.environment); err != nil {
				return err
			}
			httpClient := dependencies.HTTPClient
			if httpClient == nil {
				httpClient = http.DefaultClient
			}
			return runDataSync(ctx, dataSyncRequest{
				ProjectPath: projectPath, ProjectID: projectID, Connection: connection, ConnectionID: plan.Connection, Root: plan.Root,
				Target: credentials.Target, Token: credentials.Token, Plan: plan, Out: cmd.OutOrStdout(), HTTPClient: httpClient,
				Format: format,
			})
		},
	}
	command.Flags().StringVar(&projectPath, "project", filepath.Join("dashboards", "leapview.yaml"), "project catalog path")
	command.Flags().StringVar(&connection, "connection", "", "project-global managed connection")
	command.Flags().StringVar(&from, "from", "", "local filesystem root to ingest")
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	command.Flags().StringVar(&opts.environment, "environment", "", "assert the target instance environment")
	opts.remote.AddFlags(command)
	return command
}

// RunSync stages one planned Managed Data revision.
func RunSync(ctx context.Context, request SyncRequest) error {
	if ctx == nil || strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.Connection) == "" || strings.TrimSpace(request.Root) == "" {
		return fmt.Errorf("managed data sync requires project, connection, and source root")
	}
	connectionID := strings.TrimSpace(request.Plan.Connection)
	if connectionID == "" {
		return fmt.Errorf("managed data sync plan has no canonical connection ID")
	}
	if _, err := projectgraph.NewResourceID(connectionID); err != nil {
		return fmt.Errorf("managed data sync plan has invalid connection ID %q: %w", connectionID, err)
	}
	if request.ConnectionID != "" && strings.TrimSpace(request.ConnectionID) != connectionID {
		return fmt.Errorf("planned connection does not match sync connection")
	}
	selector := strings.TrimSpace(request.Connection)
	if request.Plan.ConnectionName != "" && selector != request.Plan.ConnectionName && selector != connectionID {
		return fmt.Errorf("planned connection does not match sync connection")
	}
	// The remainder of the transfer deliberately uses only the canonical ID;
	// the symbolic selector is retained solely for CLI validation and UX.
	request.Connection = connectionID
	if request.Format == "" {
		request.Format = "text"
	}
	if request.Format != "text" && request.Format != "json" {
		return fmt.Errorf("data sync format must be text or json")
	}
	if err := request.Plan.Manifest.Validate(manageddata.Limits{}); err != nil {
		return fmt.Errorf("planned manifest: %w", err)
	}
	if request.Plan.Manifest.RevisionID() == "" {
		return fmt.Errorf("planned manifest has no canonical revision")
	}
	wireManifest, err := manifestToWire(request.Plan.Manifest)
	if err != nil {
		return err
	}
	client := newManagedDataCLIClient(request.HTTPClient, request.Target, request.Token)
	revisionID := request.Plan.Manifest.RevisionID()
	createKey := dataSyncIdempotencyKey("create", request.ProjectID, request.Connection, revisionID)
	session, err := client.createUploadSession(ctx, request.ProjectID, request.Connection, createKey, manageddataapi.ManagedDataUploadSessionCreateRequest{Manifest: wireManifest})
	if err != nil {
		return err
	}
	if err := validateSyncSession(session, request.ProjectID, request.Connection, request.Plan.Manifest); err != nil {
		return err
	}
	if session.Status != manageddataapi.ManagedDataUploadSessionStatusCompleted {
		session, err = client.getUploadSession(ctx, request.ProjectID, request.Connection, session.Id)
		if err != nil {
			return err
		}
		if err := validateSyncSession(session, request.ProjectID, request.Connection, request.Plan.Manifest); err != nil {
			return err
		}
	}
	if terminalUploadSessionStatus(session.Status) {
		recoveryID, recoveryErr := newDataSyncRecoveryID()
		if recoveryErr != nil {
			return recoveryErr
		}
		recoveryKey := dataSyncIdempotencyKey("recover", request.ProjectID, request.Connection, revisionID, recoveryID)
		session, err = client.createUploadSession(ctx, request.ProjectID, request.Connection, recoveryKey, manageddataapi.ManagedDataUploadSessionCreateRequest{Manifest: wireManifest})
		if err != nil {
			return err
		}
		if err := validateSyncSession(session, request.ProjectID, request.Connection, request.Plan.Manifest); err != nil {
			return err
		}
	}
	if session.Status == manageddataapi.ManagedDataUploadSessionStatusFinalizing {
		session, err = waitForUploadFinalization(ctx, client, request.ProjectID, request.Connection, session.Id, request.Plan.Manifest, session)
		if err != nil {
			return err
		}
	}
	if session.Status != manageddataapi.ManagedDataUploadSessionStatusOpen && session.Status != manageddataapi.ManagedDataUploadSessionStatusCompleted {
		return fmt.Errorf("upload session is not usable: status %q", session.Status)
	}
	if session.Status != manageddataapi.ManagedDataUploadSessionStatusCompleted {
		for _, file := range request.Plan.Manifest.Files {
			upload := uploadForPath(session.Files, file.Path)
			if upload == nil {
				abortUploadSession(ctx, client, request.ProjectID, request.Connection, session.Id)
				return fmt.Errorf("upload session omitted planned file %q", file.Path)
			}
			if err := transferManagedDataFile(ctx, client, request, session.Id, file, *upload); err != nil {
				abortUploadSession(ctx, client, request.ProjectID, request.Connection, session.Id)
				return err
			}
		}
		finalized, err := client.finalizeUploadSession(ctx, request.ProjectID, request.Connection, session.Id, dataSyncIdempotencyKey("finalize", session.Id, revisionID))
		if err != nil {
			return err
		}
		if err := validateSyncSession(finalized, request.ProjectID, request.Connection, request.Plan.Manifest); err != nil {
			return err
		}
		if finalized.Status != manageddataapi.ManagedDataUploadSessionStatusCompleted {
			finalized, err = waitForUploadFinalization(ctx, client, request.ProjectID, request.Connection, session.Id, request.Plan.Manifest, finalized)
			if err != nil {
				return err
			}
		}
	}
	out := request.Out
	if out == nil {
		out = io.Discard
	}
	if request.Format == "json" {
		return json.NewEncoder(out).Encode(struct {
			SchemaVersion int    `json:"schemaVersion"`
			RevisionID    string `json:"revisionId"`
		}{SchemaVersion: 1, RevisionID: revisionID})
	}
	_, err = fmt.Fprintf(out, "staged %s\n", revisionID)
	return err
}

var runDataSync = RunSync

func terminalUploadSessionStatus(status manageddataapi.ManagedDataUploadSessionStatus) bool {
	switch status {
	case manageddataapi.ManagedDataUploadSessionStatusCancelled, manageddataapi.ManagedDataUploadSessionStatusFailed, manageddataapi.ManagedDataUploadSessionStatusExpired:
		return true
	default:
		return false
	}
}

func newDataSyncRecoveryID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate managed data recovery identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func waitForUploadFinalization(ctx context.Context, client *managedDataCLIClient, project, connection, uploadID string, manifest manageddata.Manifest, current manageddataapi.ManagedDataUploadSessionResponse) (manageddataapi.ManagedDataUploadSessionResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}
	delay := 100 * time.Millisecond
	for {
		switch current.Status {
		case manageddataapi.ManagedDataUploadSessionStatusCompleted:
			return current, nil
		case manageddataapi.ManagedDataUploadSessionStatusFinalizing:
		case manageddataapi.ManagedDataUploadSessionStatusFailed, manageddataapi.ManagedDataUploadSessionStatusCancelled, manageddataapi.ManagedDataUploadSessionStatusExpired:
			return current, fmt.Errorf("managed data upload finalization ended with status %q", current.Status)
		default:
			return current, fmt.Errorf("managed data upload finalization returned unexpected status %q", current.Status)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return current, fmt.Errorf("wait for managed data upload finalization: %w", ctx.Err())
		case <-timer.C:
		}
		next, err := client.getUploadSession(ctx, project, connection, uploadID)
		if err != nil {
			return current, err
		}
		if err := validateSyncSession(next, project, connection, manifest); err != nil {
			return current, err
		}
		current = next
		if delay < time.Second {
			delay *= 2
			if delay > time.Second {
				delay = time.Second
			}
		}
	}
}

func transferManagedDataFile(ctx context.Context, client *managedDataCLIClient, request dataSyncRequest, uploadID string, file manageddata.File, upload manageddataapi.ManagedDataFileUploadResponse) error {
	if upload.File.Path != file.Path || int64(upload.File.Size) != file.Size || upload.File.Sha256 != file.SHA256 {
		return fmt.Errorf("upload session file metadata does not match %q", file.Path)
	}
	if upload.Negotiation == nil {
		return fmt.Errorf("upload instructions are unavailable for %q", file.Path)
	}
	switch upload.Negotiation.Protocol {
	case manageddataapi.ManagedDataUploadProtocolAlreadyPresent:
		return nil
	case manageddataapi.ManagedDataUploadProtocolTus:
		if upload.Negotiation.Tus == nil || upload.Negotiation.S3Multipart != nil {
			return fmt.Errorf("invalid tus negotiation for %q", file.Path)
		}
		return uploadManagedDataTusForProject(ctx, client, request.Root, request.ProjectID, file, *upload.Negotiation.Tus)
	case manageddataapi.ManagedDataUploadProtocolS3Multipart:
		if upload.Negotiation.S3Multipart == nil || upload.Negotiation.Tus != nil {
			return fmt.Errorf("invalid S3 multipart negotiation for %q", file.Path)
		}
		return uploadManagedDataS3(ctx, client, request, uploadID, file, *upload.Negotiation.S3Multipart)
	default:
		return fmt.Errorf("unsupported upload protocol for %q", file.Path)
	}
}

func uploadManagedDataTus(ctx context.Context, client *managedDataCLIClient, root string, expected manageddata.File, negotiation manageddataapi.ManagedDataTusUploadNegotiation) error {
	return uploadManagedDataTusForProject(ctx, client, root, "", expected, negotiation)
}

func uploadManagedDataTusForProject(ctx context.Context, client *managedDataCLIClient, root, projectID string, expected manageddata.File, negotiation manageddataapi.ManagedDataTusUploadNegotiation) error {
	endpoint, err := sameOriginUploadURL(client.target, negotiation.Endpoint, negotiation.UploadId)
	if err != nil {
		return fmt.Errorf("invalid tus negotiation for %q", expected.Path)
	}
	for failures := 0; ; {
		offset, err := tusOffset(ctx, client, endpoint, expected.Size)
		if err != nil {
			if failures < dataTransferAttempts-1 && waitForTransferRetry(ctx, failures) {
				failures++
				continue
			}
			return fmt.Errorf("tus upload failed for %q", expected.Path)
		}
		if offset == expected.Size {
			return verifySourceFile(ctx, root, expected)
		}
		newOffset, retry, err := patchTusFile(ctx, client, endpoint, root, expected, offset)
		if retry {
			if failures < dataTransferAttempts-1 && waitForTransferRetry(ctx, failures) {
				failures++
				continue
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("tus upload did not complete for %q", expected.Path)
		}
		if err != nil {
			return err
		}
		if newOffset > offset && newOffset <= expected.Size {
			failures = 0
			if newOffset == expected.Size {
				return verifySourceFile(ctx, root, expected)
			}
			if err := qualificationbarrier.WaitAfterPartialTusPatch(ctx, projectID); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("tus upload returned an invalid offset for %q", expected.Path)
	}
}

func tusOffset(ctx context.Context, client *managedDataCLIClient, endpoint string, expectedSize int64) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Tus-Resumable", "1.0.0")
	request.Header.Set("Authorization", "Bearer "+client.token)
	response, err := client.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("unexpected tus status")
	}
	offset, err := strconv.ParseInt(response.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 || offset > expectedSize {
		return 0, fmt.Errorf("invalid tus offset")
	}
	if length := response.Header.Get("Upload-Length"); length != "" {
		value, err := strconv.ParseInt(length, 10, 64)
		if err != nil || value != expectedSize {
			return 0, fmt.Errorf("invalid tus length")
		}
	}
	return offset, nil
}

func patchTusFile(ctx context.Context, client *managedDataCLIClient, endpoint, root string, expected manageddata.File, offset int64) (int64, bool, error) {
	file, snapshot, err := openExpectedSource(root, expected)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, false, sourceChanged(expected.Path)
	}
	remaining := expected.Size - offset
	chunkSize := min(remaining, int64(dataTusChunkSize))
	counted := &countingReader{reader: io.LimitReader(file, chunkSize)}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, io.NopCloser(counted))
	if err != nil {
		return 0, false, fmt.Errorf("build tus request for %q", expected.Path)
	}
	request.ContentLength = chunkSize
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Tus-Resumable", "1.0.0")
	request.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	request.Header.Set("Content-Type", "application/offset+octet-stream")
	response, err := client.http.Do(request)
	if err != nil {
		return 0, true, fmt.Errorf("tus upload request failed for %q", expected.Path)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode == http.StatusConflict || response.StatusCode >= 500 {
		return 0, true, fmt.Errorf("tus upload failed for %q with HTTP %d", expected.Path, response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, false, fmt.Errorf("tus upload failed for %q with HTTP %d", expected.Path, response.StatusCode)
	}
	if counted.count != chunkSize {
		return 0, false, sourceChanged(expected.Path)
	}
	if err := validateOpenSource(file, snapshot, root, expected); err != nil {
		return 0, false, err
	}
	rawOffset := response.Header.Get("Upload-Offset")
	newOffset, err := strconv.ParseInt(rawOffset, 10, 64)
	if err != nil || newOffset != offset+chunkSize {
		return 0, false, fmt.Errorf("tus upload returned an invalid offset for %q: expected %d, got %q", expected.Path, offset+chunkSize, rawOffset)
	}
	return newOffset, false, nil
}

func uploadManagedDataS3(ctx context.Context, client *managedDataCLIClient, request dataSyncRequest, uploadID string, expected manageddata.File, negotiation manageddataapi.ManagedDataS3MultipartNegotiation) error {
	if err := verifySourceFile(ctx, request.Root, expected); err != nil {
		return err
	}
	partSize, err := deterministicPartSize(expected.Size, negotiation)
	if err != nil {
		return fmt.Errorf("invalid S3 multipart negotiation for %q", expected.Path)
	}
	createKey := dataSyncIdempotencyKey("multipart-create", uploadID, expected.Path, expected.SHA256)
	multipart, err := client.createMultipart(ctx, request.ProjectID, request.Connection, uploadID, createKey, expected.Path)
	if err != nil {
		return err
	}
	if multipart.UploadSessionId != uploadID || multipart.File.Path != expected.Path || multipart.File.Sha256 != expected.SHA256 || int64(multipart.File.Size) != expected.Size {
		return fmt.Errorf("multipart upload metadata does not match %q", expected.Path)
	}
	if multipart.Existing && multipart.Status == manageddataapi.ManagedDataS3MultipartStatusCompleted {
		return nil
	}
	completed := false
	defer func() {
		if !completed {
			abortMultipart(ctx, client, request.ProjectID, request.Connection, uploadID, multipart.Id)
		}
	}()
	parts := make([]manageddataapi.ManagedDataS3CompletedPart, 0, (expected.Size+partSize-1)/partSize)
	for offset, partNumber := int64(0), int32(1); offset < expected.Size; offset, partNumber = offset+partSize, partNumber+1 {
		size := min(partSize, expected.Size-offset)
		partDigest, err := hashSourceRange(ctx, request.Root, expected, offset, size)
		if err != nil {
			return err
		}
		signed, err := client.signMultipartPart(ctx, request.ProjectID, request.Connection, uploadID, multipart.Id, partNumber, manageddataapi.ManagedDataS3MultipartSignPartRequest{Size: size, Sha256: &partDigest})
		if err != nil {
			return err
		}
		if signed.PartNumber != partNumber || strings.TrimSpace(signed.Url) == "" {
			return fmt.Errorf("invalid signed S3 part response for %q", expected.Path)
		}
		etag, err := putSignedPart(ctx, client.http, signed, request.Root, expected, offset, size, partDigest)
		if err != nil {
			return err
		}
		parts = append(parts, manageddataapi.ManagedDataS3CompletedPart{PartNumber: partNumber, Etag: etag, Sha256: &partDigest})
	}
	if err := verifySourceFile(ctx, request.Root, expected); err != nil {
		return err
	}
	result, err := client.completeMultipart(ctx, request.ProjectID, request.Connection, uploadID, multipart.Id, dataSyncIdempotencyKey("multipart-complete", multipart.Id, expected.SHA256), manageddataapi.ManagedDataS3MultipartCompleteRequest{Parts: parts})
	if err != nil {
		return err
	}
	if result.Id != multipart.Id || result.Status != manageddataapi.ManagedDataS3MultipartStatusCompleted {
		return fmt.Errorf("S3 multipart upload did not complete for %q", expected.Path)
	}
	completed = true
	return nil
}

func deterministicPartSize(size int64, negotiation manageddataapi.ManagedDataS3MultipartNegotiation) (int64, error) {
	minimum, maximum, maximumParts := int64(negotiation.MinimumPartSize), int64(negotiation.MaximumPartSize), int64(negotiation.MaximumParts)
	if size <= 0 || minimum <= 0 || maximum < minimum || maximumParts <= 0 || maximumParts > 10_000 {
		return 0, fmt.Errorf("invalid multipart limits")
	}
	required := (size + maximumParts - 1) / maximumParts
	partSize := max(minimum, required)
	if partSize > maximum || (size+partSize-1)/partSize > maximumParts {
		return 0, fmt.Errorf("file cannot fit multipart limits")
	}
	return partSize, nil
}

func putSignedPart(ctx context.Context, client *http.Client, signed manageddataapi.ManagedDataS3MultipartSignedPartResponse, root string, expected manageddata.File, offset, size int64, expectedPartDigest string) (string, error) {
	if _, err := validateSignedURL(signed.Url); err != nil {
		return "", fmt.Errorf("invalid signed S3 part response for %q", expected.Path)
	}
	for attempt := 0; attempt < dataTransferAttempts; attempt++ {
		file, snapshot, err := openExpectedSource(root, expected)
		if err != nil {
			return "", err
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return "", sourceChanged(expected.Path)
		}
		digest := sha256.New()
		counted := &countingReader{reader: io.TeeReader(io.LimitReader(file, size), digest)}
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.Url, io.NopCloser(counted))
		if err != nil {
			file.Close()
			return "", fmt.Errorf("build signed S3 part request for %q", expected.Path)
		}
		request.ContentLength = size
		if err := applySignedHeaders(request, signed.Headers, size); err != nil {
			file.Close()
			return "", fmt.Errorf("invalid signed S3 part response for %q", expected.Path)
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			file.Close()
			if waitForTransferRetry(ctx, attempt) {
				continue
			}
			return "", fmt.Errorf("signed S3 part upload failed for %q", expected.Path)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		stateErr := validateOpenSource(file, snapshot, root, expected)
		file.Close()
		if stateErr != nil {
			return "", stateErr
		}
		if response.StatusCode >= 500 && waitForTransferRetry(ctx, attempt) {
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return "", fmt.Errorf("signed S3 part upload failed for %q with HTTP %d", expected.Path, response.StatusCode)
		}
		if counted.count != size || hex.EncodeToString(digest.Sum(nil)) != expectedPartDigest {
			return "", sourceChanged(expected.Path)
		}
		etag := strings.TrimSpace(response.Header.Get("ETag"))
		if etag == "" || len(etag) > 1024 || strings.ContainsAny(etag, "\x00\r\n") {
			return "", fmt.Errorf("signed S3 part response omitted a valid ETag for %q", expected.Path)
		}
		return etag, nil
	}
	return "", fmt.Errorf("signed S3 part upload retries exhausted for %q", expected.Path)
}

func verifySourceFile(ctx context.Context, root string, expected manageddata.File) error {
	file, snapshot, err := openExpectedSource(root, expected)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	count, err := io.Copy(digest, contextReader{ctx: ctx, reader: file})
	if err != nil || count != expected.Size || hex.EncodeToString(digest.Sum(nil)) != expected.SHA256 {
		return sourceChanged(expected.Path)
	}
	return validateOpenSource(file, snapshot, root, expected)
}

func hashSourceRange(ctx context.Context, root string, expected manageddata.File, offset, size int64) (string, error) {
	file, snapshot, err := openExpectedSource(root, expected)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", sourceChanged(expected.Path)
	}
	digest := sha256.New()
	count, err := io.CopyN(digest, contextReader{ctx: ctx, reader: file}, size)
	if err != nil || count != size {
		return "", sourceChanged(expected.Path)
	}
	if err := validateOpenSource(file, snapshot, root, expected); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type sourceSnapshot struct {
	path string
	info os.FileInfo
}

func openExpectedSource(root string, expected manageddata.File) (*os.File, sourceSnapshot, error) {
	if err := validateSourcePath(root, expected.Path); err != nil {
		return nil, sourceSnapshot{}, sourceChanged(expected.Path)
	}
	name := filepath.Join(root, filepath.FromSlash(expected.Path))
	before, err := os.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() != expected.Size {
		return nil, sourceSnapshot{}, sourceChanged(expected.Path)
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, sourceSnapshot{}, sourceChanged(expected.Path)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !sameSourceState(before, opened) {
		file.Close()
		return nil, sourceSnapshot{}, sourceChanged(expected.Path)
	}
	return file, sourceSnapshot{path: name, info: before}, nil
}

func validateOpenSource(file *os.File, snapshot sourceSnapshot, root string, expected manageddata.File) error {
	opened, openErr := file.Stat()
	pathInfo, pathErr := os.Lstat(snapshot.path)
	if openErr != nil || pathErr != nil || !sameSourceState(snapshot.info, opened) || !sameSourceState(snapshot.info, pathInfo) || !os.SameFile(snapshot.info, pathInfo) {
		return sourceChanged(expected.Path)
	}
	if err := validateSourcePath(root, expected.Path); err != nil {
		return sourceChanged(expected.Path)
	}
	return nil
}

func validateSourcePath(root, logicalPath string) error {
	if root == "" || logicalPath == "" || filepath.IsAbs(filepath.FromSlash(logicalPath)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(logicalPath))) != logicalPath {
		return fmt.Errorf("invalid path")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("invalid root")
	}
	current := root
	parts := strings.Split(filepath.FromSlash(logicalPath), string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || index < len(parts)-1 && !info.IsDir() || index == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe path")
		}
	}
	return nil
}

func sameSourceState(left, right os.FileInfo) bool {
	return left.Size() == right.Size() && left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func sourceChanged(path string) error {
	return fmt.Errorf("source file %q changed since planning", path)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.count += int64(count)
	return count, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func manifestToWire(manifest manageddata.Manifest) (manageddataapi.ManagedDataManifest, error) {
	files := make([]manageddataapi.ManagedDataFileMetadata, len(manifest.Files))
	for index, file := range manifest.Files {
		files[index] = manageddataapi.ManagedDataFileMetadata{Path: file.Path, Size: file.Size, Sha256: file.SHA256}
	}
	return manageddataapi.ManagedDataManifest{Files: files}, nil
}

func validateSyncSession(session manageddataapi.ManagedDataUploadSessionResponse, project, connection string, manifest manageddata.Manifest) error {
	if session.Id == "" || session.Project != project || session.Connection != connection || session.RevisionId != manifest.RevisionID() {
		return fmt.Errorf("upload session identity does not match the planned revision")
	}
	wire, err := manifestToWire(manifest)
	if err != nil {
		return err
	}
	if len(session.Manifest.Files) != len(wire.Files) {
		return fmt.Errorf("upload session manifest does not match the planned revision")
	}
	for index := range wire.Files {
		if session.Manifest.Files[index] != wire.Files[index] {
			return fmt.Errorf("upload session manifest does not match the planned revision")
		}
	}
	if len(session.Files) != len(manifest.Files) {
		return fmt.Errorf("upload session file set does not match the planned revision")
	}
	seen := make(map[string]struct{}, len(session.Files))
	for _, upload := range session.Files {
		if _, exists := seen[upload.File.Path]; exists {
			return fmt.Errorf("upload session file set does not match the planned revision")
		}
		seen[upload.File.Path] = struct{}{}
		planned := uploadForManagedPath(manifest.Files, upload.File.Path)
		if planned == nil || upload.File.Sha256 != planned.SHA256 || int64(upload.File.Size) != planned.Size {
			return fmt.Errorf("upload session file set does not match the planned revision")
		}
	}
	return nil
}

func uploadForManagedPath(files []manageddata.File, logicalPath string) *manageddata.File {
	for index := range files {
		if files[index].Path == logicalPath {
			return &files[index]
		}
	}
	return nil
}

func uploadForPath(files []manageddataapi.ManagedDataFileUploadResponse, logicalPath string) *manageddataapi.ManagedDataFileUploadResponse {
	for index := range files {
		if files[index].File.Path == logicalPath {
			return &files[index]
		}
	}
	return nil
}

func dataSyncIdempotencyKey(kind string, values ...string) string {
	digest := sha256.New()
	writeHashValue(digest, kind)
	for _, value := range values {
		writeHashValue(digest, value)
	}
	return "data-sync-" + kind + "-" + hex.EncodeToString(digest.Sum(nil))
}

func writeHashValue(digest hash.Hash, value string) {
	_, _ = io.WriteString(digest, value)
	_, _ = digest.Write([]byte{0})
}

func sameOriginUploadURL(target, endpoint, uploadID string) (string, error) {
	base, err := url.Parse(target)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid target")
	}
	reference, err := url.Parse(endpoint)
	if err != nil || reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" {
		return "", fmt.Errorf("invalid endpoint")
	}
	resolved := base.ResolveReference(reference)
	if !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) {
		return "", fmt.Errorf("cross-origin endpoint")
	}
	resolved.Path = strings.TrimRight(resolved.Path, "/") + "/" + url.PathEscape(uploadID)
	return resolved.String(), nil
}

func validateSignedURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid signed URL")
	}
	return parsed, nil
}

func applySignedHeaders(request *http.Request, headers []manageddataapi.ManagedDataHTTPHeader, size int64) error {
	seen := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		name := http.CanonicalHeaderKey(strings.TrimSpace(header.Name))
		if name == "" || strings.ContainsAny(name, "\x00\r\n:") || strings.ContainsAny(header.Value, "\x00\r\n") {
			return fmt.Errorf("invalid signed header")
		}
		folded := strings.ToLower(name)
		if _, exists := seen[folded]; exists {
			return fmt.Errorf("duplicate signed header")
		}
		seen[folded] = struct{}{}
		switch folded {
		case "authorization", "cookie", "proxy-authorization", "connection", "transfer-encoding":
			return fmt.Errorf("unsafe signed header")
		case "host":
			if !strings.EqualFold(strings.TrimSpace(header.Value), request.URL.Host) {
				return fmt.Errorf("signed host does not match upload URL")
			}
			request.Host = request.URL.Host
		case "content-length":
			value, err := strconv.ParseInt(strings.TrimSpace(header.Value), 10, 64)
			if err != nil || value != size {
				return fmt.Errorf("signed content length does not match part")
			}
		default:
			request.Header.Set(name, header.Value)
		}
	}
	return nil
}

func waitForTransferRetry(ctx context.Context, attempt int) bool {
	if attempt+1 >= dataTransferAttempts {
		return false
	}
	timer := time.NewTimer(time.Duration(attempt+1) * 20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func abortUploadSession(ctx context.Context, client *managedDataCLIClient, project, connection, uploadID string) {
	abort, cancel := abortContext(ctx)
	defer cancel()
	client.abortUploadSession(abort, project, connection, uploadID, dataSyncIdempotencyKey("abort", uploadID))
}

func abortMultipart(ctx context.Context, client *managedDataCLIClient, project, connection, uploadID, multipartID string) {
	abort, cancel := abortContext(ctx)
	defer cancel()
	client.abortMultipart(abort, project, connection, uploadID, multipartID, dataSyncIdempotencyKey("multipart-abort", multipartID))
}

func abortContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 10*time.Second)
}
