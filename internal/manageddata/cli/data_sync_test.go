package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddataapi "github.com/flidai/leapview/internal/manageddata/api"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/manageddata/qualificationbarrier"
)

func TestDataSyncDeduplicatesAndUsesStableIdempotencyKey(t *testing.T) {
	root := t.TempDir()
	file := writeSyncFile(t, root, "orders.csv", []byte("order_id\n1\n"))
	plan := syncPlan(root, file)
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/upload-sessions") {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "/connections/connection:orders/") {
			t.Fatalf("request used symbolic connection instead of canonical ID: %s", r.URL.Path)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		writeCreatedUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusCompleted, []manageddataapi.ManagedDataFileUploadResponse{{
			File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusSkipped,
			Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolAlreadyPresent}),
		}})
	}))
	defer server.Close()

	for range 2 {
		var out bytes.Buffer
		err := runDataSync(context.Background(), dataSyncRequest{
			ProjectPath: "/catalog/leapview.yaml", ProjectID: "demo", Connection: "orders", Root: root,
			Target: server.URL, Token: "secret-token", Plan: plan, Out: &out, HTTPClient: server.Client(),
		})
		if err != nil {
			t.Fatalf("runDataSync() error = %v", err)
		}
		if got, want := out.String(), "staged "+plan.Manifest.RevisionID()+"\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency keys = %#v", keys)
	}
}

func TestDataSyncRequiresCanonicalPlannedConnectionID(t *testing.T) {
	for _, test := range []struct {
		name       string
		connection string
	}{
		{name: "missing"},
		{name: "invalid", connection: "connection with spaces"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runDataSync(context.Background(), dataSyncRequest{
				ProjectID: "project:demo", Connection: "orders", Root: t.TempDir(),
				Plan: localplan.Result{Connection: test.connection},
			})
			if err == nil || !strings.Contains(err.Error(), "connection ID") {
				t.Fatalf("runDataSync() error = %v, want canonical connection ID failure", err)
			}
		})
	}
}

func TestDataSyncResumesTusFromHEADOffset(t *testing.T) {
	root := t.TempDir()
	body := []byte("0123456789")
	file := writeSyncFile(t, root, "orders.csv", body)
	plan := syncPlan(root, file)
	var mu sync.Mutex
	offset := int64(4)
	var patched []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-sessions"),
			r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upload-sessions/upload-1"):
			writeUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusOpen, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusUploading,
				Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolTus, Tus: &manageddataapi.ManagedDataTusUploadNegotiation{Endpoint: "/tus", UploadId: "blob-1", Offset: 0, ExpiresAt: "2030-01-01T00:00:00Z"}}),
			}})
		case r.URL.Path == "/tus/blob-1" && r.Method == http.MethodHead:
			mu.Lock()
			current := offset
			mu.Unlock()
			w.Header().Set("Tus-Resumable", "1.0.0")
			w.Header().Set("Upload-Offset", strconv.FormatInt(current, 10))
			w.Header().Set("Upload-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/tus/blob-1" && r.Method == http.MethodPatch:
			if got := r.Header.Get("Upload-Offset"); got != "4" {
				t.Fatalf("Upload-Offset = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
				t.Fatalf("Authorization = %q", got)
			}
			chunk, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			patched = append(patched, chunk...)
			offset += int64(len(chunk))
			current := offset
			mu.Unlock()
			w.Header().Set("Tus-Resumable", "1.0.0")
			w.Header().Set("Upload-Offset", strconv.FormatInt(current, 10))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			writeUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusCompleted, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusVerified,
				Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolAlreadyPresent}),
			}})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := runDataSync(context.Background(), dataSyncRequest{ProjectID: "demo", Connection: "orders", Root: root, Target: server.URL, Token: "secret-token", Plan: plan, Out: io.Discard, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("runDataSync() error = %v", err)
	}
	if got, want := string(patched), string(body[4:]); got != want {
		t.Fatalf("patched = %q, want %q", got, want)
	}
}

func TestDataSyncQualificationBarrierPausesAfterFirstTusChunk(t *testing.T) {
	root := t.TempDir()
	body := make([]byte, dataTusChunkSize+1)
	for index := range body {
		body[index] = byte(index)
	}
	file := writeSyncFile(t, root, "orders.csv", body)
	plan := syncPlan(root, file)
	barrierDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(barrierDir, qualificationbarrier.ArmedMarker), []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(qualificationbarrier.EnabledEnv, qualificationbarrier.EnabledValue)
	t.Setenv(qualificationbarrier.PathEnv, barrierDir)
	t.Setenv(qualificationbarrier.ProjectIDEnv, qualificationbarrier.EvaluationProjectID)
	var mu sync.Mutex
	offset := int64(0)
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSession := func(status manageddataapi.ManagedDataUploadSessionStatus, files []manageddataapi.ManagedDataFileUploadResponse) {
			response := uploadSessionResponse(plan, "upload-1", status, files)
			response.Project = qualificationbarrier.EvaluationProjectID
			writeJSONTest(t, w, http.StatusCreated, response)
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-sessions"),
			r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upload-sessions/upload-1"):
			writeSession(manageddataapi.ManagedDataUploadSessionStatusOpen, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusUploading,
				Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolTus, Tus: &manageddataapi.ManagedDataTusUploadNegotiation{Endpoint: "/tus", UploadId: "blob-1", Offset: 0, ExpiresAt: "2030-01-01T00:00:00Z"}}),
			}})
		case r.Method == http.MethodHead && r.URL.Path == "/tus/blob-1":
			mu.Lock()
			current := offset
			mu.Unlock()
			w.Header().Set("Upload-Offset", strconv.FormatInt(current, 10))
			w.Header().Set("Upload-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPatch && r.URL.Path == "/tus/blob-1":
			chunk, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read PATCH body: %v", err)
				return
			}
			mu.Lock()
			if got, want := r.Header.Get("Upload-Offset"), strconv.FormatInt(offset, 10); got != want {
				mu.Unlock()
				t.Errorf("PATCH Upload-Offset = %q, want %q", got, want)
				return
			}
			offset += int64(len(chunk))
			patches++
			current := offset
			mu.Unlock()
			w.Header().Set("Upload-Offset", strconv.FormatInt(current, 10))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			writeSession(manageddataapi.ManagedDataUploadSessionStatusCompleted, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusVerified,
				Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolAlreadyPresent}),
			}})
		default:
			t.Errorf("unexpected request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- runDataSync(ctx, dataSyncRequest{ProjectID: qualificationbarrier.EvaluationProjectID, Connection: "orders", Root: root, Target: server.URL, Token: "secret-token", Plan: plan, Out: io.Discard, HTTPClient: server.Client()})
	}()
	reached := filepath.Join(barrierDir, qualificationbarrier.ReachedMarker)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(reached); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("managed upload barrier was not reached")
		}
		select {
		case err := <-result:
			t.Fatalf("sync completed before barrier: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	if patches != 1 || offset != dataTusChunkSize {
		t.Fatalf("partial upload state = patches %d, offset %d; want one patch at %d", patches, offset, dataTusChunkSize)
	}
	mu.Unlock()
	select {
	case err := <-result:
		t.Fatalf("sync completed while barrier was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.Remove(reached); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("runDataSync() error = %v", err)
	}
	mu.Lock()
	finalPatches, finalOffset := patches, offset
	mu.Unlock()
	if finalPatches != 2 || finalOffset != int64(len(body)) {
		t.Fatalf("final upload state = patches %d, offset %d; want two patches at %d", finalPatches, finalOffset, len(body))
	}
}

func TestDataSyncWaitsForAsynchronousFinalization(t *testing.T) {
	root := t.TempDir()
	file := writeSyncFile(t, root, "orders.csv", []byte("order_id\n1\n"))
	plan := syncPlan(root, file)
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files := []manageddataapi.ManagedDataFileUploadResponse{{
			File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusSkipped,
			Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolAlreadyPresent}),
		}}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-sessions"):
			writeUploadSession(t, w, plan, "upload-async", manageddataapi.ManagedDataUploadSessionStatusOpen, files)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			writeUploadSession(t, w, plan, "upload-async", manageddataapi.ManagedDataUploadSessionStatusFinalizing, files)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upload-sessions/upload-async"):
			getCalls++
			status := manageddataapi.ManagedDataUploadSessionStatusFinalizing
			if getCalls == 2 {
				status = manageddataapi.ManagedDataUploadSessionStatusCompleted
			}
			writeUploadSession(t, w, plan, "upload-async", status, files)
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := runDataSync(context.Background(), dataSyncRequest{ProjectID: "demo", Connection: "orders", Root: root, Target: server.URL, Token: "secret-token", Plan: plan, Out: io.Discard, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("runDataSync() error = %v", err)
	}
	if getCalls != 2 {
		t.Fatalf("upload status GET calls = %d, want 2", getCalls)
	}
}

func TestDataSyncReplacesAReplayedTerminalUploadSession(t *testing.T) {
	root := t.TempDir()
	file := writeSyncFile(t, root, "orders.csv", []byte("order_id\n1\n"))
	plan := syncPlan(root, file)
	var createKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-sessions"):
			createKeys = append(createKeys, r.Header.Get("Idempotency-Key"))
			if len(createKeys) == 1 {
				writeUploadSession(t, w, plan, "upload-stale", manageddataapi.ManagedDataUploadSessionStatusOpen, []manageddataapi.ManagedDataFileUploadResponse{{
					File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusUploading,
					Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolTus, Tus: &manageddataapi.ManagedDataTusUploadNegotiation{Endpoint: "/tus", UploadId: "missing", ExpiresAt: "2030-01-01T00:00:00Z"}}),
				}})
				return
			}
			writeCreatedUploadSession(t, w, plan, "upload-replacement", manageddataapi.ManagedDataUploadSessionStatusCompleted, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusSkipped,
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upload-sessions/upload-stale"):
			writeUploadSession(t, w, plan, "upload-stale", manageddataapi.ManagedDataUploadSessionStatusCancelled, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusSkipped,
			}})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := runDataSync(context.Background(), dataSyncRequest{ProjectID: "demo", Connection: "orders", Root: root, Target: server.URL, Token: "secret-token", Plan: plan, Out: io.Discard, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("runDataSync() error = %v", err)
	}
	if len(createKeys) != 2 || createKeys[0] == "" || createKeys[1] == "" || createKeys[0] == createKeys[1] {
		t.Fatalf("create idempotency keys = %#v", createKeys)
	}
}

func TestDataSyncRetriesTusCapacityFailureAndReportsHTTPStatus(t *testing.T) {
	root := t.TempDir()
	body := []byte("orders")
	file := writeSyncFile(t, root, "orders.csv", body)
	var patchAttempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Tus-Resumable", "1.0.0")
			w.Header().Set("Upload-Offset", "0")
			w.Header().Set("Upload-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPatch:
			patchAttempts++
			http.Error(w, "capacity details must not be exposed", http.StatusInsufficientStorage)
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newManagedDataCLIClient(server.Client(), server.URL, "secret-token")
	err := uploadManagedDataTus(context.Background(), client, root, file, manageddataapi.ManagedDataTusUploadNegotiation{
		Endpoint: "/tus", UploadId: "blob-1", ExpiresAt: "2030-01-01T00:00:00Z",
	})
	if err == nil || !strings.Contains(err.Error(), `tus upload failed for "orders.csv" with HTTP 507`) {
		t.Fatalf("uploadManagedDataTus() error = %v", err)
	}
	if strings.Contains(err.Error(), "capacity details") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error exposed secret material: %v", err)
	}
	if patchAttempts != dataTransferAttempts {
		t.Fatalf("PATCH attempts = %d, want %d", patchAttempts, dataTransferAttempts)
	}
}

func TestDataSyncRejectsMissingUploadInstructionsWithoutPanicking(t *testing.T) {
	file := manageddata.File{Path: "orders.csv", Size: 3, SHA256: strings.Repeat("a", 64)}
	err := transferManagedDataFile(context.Background(), nil, dataSyncRequest{}, "upload-1", file, manageddataapi.ManagedDataFileUploadResponse{
		File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusPending,
	})
	if err == nil || !strings.Contains(err.Error(), "upload instructions are unavailable") {
		t.Fatalf("missing negotiation error = %v", err)
	}
}

func TestDataSyncUploadsDeterministicS3PartsWithoutBearerToken(t *testing.T) {
	root := t.TempDir()
	body := []byte("abcdefghij")
	file := writeSyncFile(t, root, "orders.csv", body)
	plan := syncPlan(root, file)
	var uploaded [][]byte
	var signedSizes []int64
	var completed manageddataapi.ManagedDataS3MultipartCompleteRequest
	var mutationKeys []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-sessions"),
			r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upload-sessions/upload-1"):
			if r.Method == http.MethodPost {
				mutationKeys = append(mutationKeys, r.Header.Get("Idempotency-Key"))
			}
			writeUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusOpen, []manageddataapi.ManagedDataFileUploadResponse{{
				File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusPending,
				Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolS3Multipart, S3Multipart: &manageddataapi.ManagedDataS3MultipartNegotiation{CreateEndpoint: "/unused", MinimumPartSize: 4, MaximumPartSize: 6, MaximumParts: 3}}),
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/s3-multipart-uploads"):
			mutationKeys = append(mutationKeys, r.Header.Get("Idempotency-Key"))
			writeJSONTest(t, w, http.StatusCreated, manageddataapi.ManagedDataS3MultipartUploadResponse{Id: "multipart-1", UploadSessionId: "upload-1", File: wireFile(t, file), Status: manageddataapi.ManagedDataS3MultipartStatusOpen, CreatedAt: "2026-01-01T00:00:00Z"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/parts/") && strings.HasSuffix(r.URL.Path, "/sign"):
			var request manageddataapi.ManagedDataS3MultipartSignPartRequest
			decodeJSONTest(t, r, &request)
			signedSizes = append(signedSizes, request.Size)
			partNumber, _ := strconv.Atoi(strings.Split(r.URL.Path, "/")[len(strings.Split(r.URL.Path, "/"))-2])
			writeJSONTest(t, w, http.StatusOK, manageddataapi.ManagedDataS3MultipartSignedPartResponse{PartNumber: int32(partNumber), Url: fmt.Sprintf("%s/signed/%d?signature=must-not-leak", server.URL, partNumber), Headers: []manageddataapi.ManagedDataHTTPHeader{{Name: "x-test", Value: "signed"}}, ExpiresAt: "2030-01-01T00:00:00Z"})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/signed/"):
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("signed PUT received Authorization = %q", got)
			}
			part, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			uploaded = append(uploaded, part)
			w.Header().Set("ETag", fmt.Sprintf("etag-%d", len(uploaded)))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete"):
			mutationKeys = append(mutationKeys, r.Header.Get("Idempotency-Key"))
			decodeJSONTest(t, r, &completed)
			writeJSONTest(t, w, http.StatusOK, manageddataapi.ManagedDataS3MultipartUploadResponse{Id: "multipart-1", UploadSessionId: "upload-1", File: wireFile(t, file), Status: manageddataapi.ManagedDataS3MultipartStatusCompleted, CreatedAt: "2026-01-01T00:00:00Z"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			mutationKeys = append(mutationKeys, r.Header.Get("Idempotency-Key"))
			writeUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusCompleted, []manageddataapi.ManagedDataFileUploadResponse{{File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusVerified, Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolAlreadyPresent})}})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	err := runDataSync(context.Background(), dataSyncRequest{ProjectID: "demo", Connection: "orders", Root: root, Target: server.URL, Token: "secret-token", Plan: plan, Out: io.Discard, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("runDataSync() error = %v", err)
	}
	if got := strings.Join([]string{string(uploaded[0]), string(uploaded[1]), string(uploaded[2])}, ""); got != string(body) {
		t.Fatalf("uploaded = %q", got)
	}
	if fmt.Sprint(signedSizes) != "[4 4 2]" {
		t.Fatalf("signed sizes = %v", signedSizes)
	}
	if len(completed.Parts) != 3 || completed.Parts[0].Etag != "etag-1" || completed.Parts[2].PartNumber != 3 {
		t.Fatalf("completed parts = %#v", completed.Parts)
	}
	if len(mutationKeys) != 4 {
		t.Fatalf("mutation keys = %#v", mutationKeys)
	}
	for _, key := range mutationKeys {
		if key == "" {
			t.Fatalf("mutation keys = %#v", mutationKeys)
		}
	}
}

func TestDataSyncDetectsMutationAndSanitizesSignedURL(t *testing.T) {
	root := t.TempDir()
	file := writeSyncFile(t, root, "orders.csv", []byte("before"))
	plan := syncPlan(root, file)
	if err := os.WriteFile(filepath.Join(root, file.Path), []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	var aborted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-sessions"),
			r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upload-sessions/upload-1"):
			writeUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusOpen, []manageddataapi.ManagedDataFileUploadResponse{{File: wireFile(t, file), Status: manageddataapi.ManagedDataFileUploadStatusPending, Negotiation: uploadNegotiation(manageddataapi.ManagedDataUploadNegotiation{Protocol: manageddataapi.ManagedDataUploadProtocolS3Multipart, S3Multipart: &manageddataapi.ManagedDataS3MultipartNegotiation{MinimumPartSize: 4, MaximumPartSize: 6, MaximumParts: 3}})}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"):
			aborted = true
			writeUploadSession(t, w, plan, "upload-1", manageddataapi.ManagedDataUploadSessionStatusCancelled, nil)
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := runDataSync(context.Background(), dataSyncRequest{ProjectID: "demo", Connection: "orders", Root: root, Target: server.URL, Token: "secret-token", Plan: plan, Out: io.Discard, HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "changed since planning") {
		t.Fatalf("runDataSync() error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "signature=") {
		t.Fatalf("error exposed secret material: %v", err)
	}
	if !aborted {
		t.Fatal("upload session was not aborted")
	}
}

func TestSignedPartFailureDoesNotExposeSignedURL(t *testing.T) {
	root := t.TempDir()
	file := writeSyncFile(t, root, "orders.csv", []byte("part"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "signature=also-secret", http.StatusForbidden)
	}))
	defer server.Close()

	signedURL := server.URL + "/part?signature=must-not-leak"
	_, err := putSignedPart(context.Background(), server.Client(), manageddataapi.ManagedDataS3MultipartSignedPartResponse{PartNumber: 1, Url: signedURL}, root, file, 0, file.Size, file.SHA256)
	if err == nil {
		t.Fatal("putSignedPart() error = nil")
	}
	if strings.Contains(err.Error(), "signature=") || strings.Contains(err.Error(), signedURL) {
		t.Fatalf("error exposed signed URL: %v", err)
	}
}

func syncPlan(root string, file manageddata.File) localplan.Result {
	return localplan.Result{Connection: "connection:orders", ConnectionName: "orders", Root: root, Manifest: manageddata.Manifest{Files: []manageddata.File{file}}}
}

func uploadNegotiation(value manageddataapi.ManagedDataUploadNegotiation) *manageddataapi.ManagedDataUploadNegotiation {
	return &value
}

func writeSyncFile(t *testing.T, root, name string, body []byte) manageddata.File {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return manageddata.File{Path: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

func wireFile(t *testing.T, file manageddata.File) manageddataapi.ManagedDataFileMetadata {
	t.Helper()
	return manageddataapi.ManagedDataFileMetadata{Path: file.Path, Size: file.Size, Sha256: file.SHA256}
}

func writeUploadSession(t *testing.T, w http.ResponseWriter, plan localplan.Result, id string, status manageddataapi.ManagedDataUploadSessionStatus, files []manageddataapi.ManagedDataFileUploadResponse) {
	t.Helper()
	writeJSONTest(t, w, map[manageddataapi.ManagedDataUploadSessionStatus]int{manageddataapi.ManagedDataUploadSessionStatusOpen: http.StatusCreated}[status], uploadSessionResponse(plan, id, status, files))
}

func writeCreatedUploadSession(t *testing.T, w http.ResponseWriter, plan localplan.Result, id string, status manageddataapi.ManagedDataUploadSessionStatus, files []manageddataapi.ManagedDataFileUploadResponse) {
	t.Helper()
	writeJSONTest(t, w, http.StatusCreated, uploadSessionResponse(plan, id, status, files))
}

func uploadSessionResponse(plan localplan.Result, id string, status manageddataapi.ManagedDataUploadSessionStatus, files []manageddataapi.ManagedDataFileUploadResponse) manageddataapi.ManagedDataUploadSessionResponse {
	wFiles := make([]manageddataapi.ManagedDataFileMetadata, len(plan.Manifest.Files))
	for i, file := range plan.Manifest.Files {
		wFiles[i] = manageddataapi.ManagedDataFileMetadata{Path: file.Path, Size: file.Size, Sha256: file.SHA256}
	}
	return manageddataapi.ManagedDataUploadSessionResponse{
		Id: id, Project: "demo", Connection: plan.Connection, RevisionId: plan.Manifest.RevisionID(), Status: status,
		Manifest: manageddataapi.ManagedDataManifest{Files: wFiles}, Files: files, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2030-01-01T00:00:00Z",
	}
}

func writeJSONTest(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	if status == 0 {
		status = http.StatusAccepted
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func decodeJSONTest(t *testing.T, r *http.Request, value any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		t.Fatal(err)
	}
}
