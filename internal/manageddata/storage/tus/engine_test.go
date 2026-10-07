package tus_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	managedtus "github.com/flidai/leapview/internal/manageddata/storage/tus"
)

func TestEngineCreateResumeWriteAndFinalize(t *testing.T) {
	engine, blobs, uploadRoot := newEngine(t)
	body := []byte("resumable upload")
	expected := blobFor(body)

	created, err := engine.Create(t.Context(), storage.CreateUpload{ID: "upload-1", Size: int64(len(body)), Metadata: map[string]string{"filename": "orders.csv"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "upload-1" || created.Offset != 0 || created.Size != int64(len(body)) {
		t.Fatalf("Create() = %#v", created)
	}
	for _, name := range []string{"upload-1", "upload-1.info"} {
		info, err := os.Stat(filepath.Join(uploadRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", name, got)
		}
	}

	partial := body[:5]
	written, err := engine.WriteChunk(t.Context(), created.ID, 0, bytes.NewReader(partial))
	if err != nil {
		t.Fatal(err)
	}
	if written.Offset != int64(len(partial)) {
		t.Fatalf("WriteChunk() offset = %d", written.Offset)
	}
	if _, err := engine.WriteChunk(t.Context(), created.ID, 0, bytes.NewReader(body)); !errors.Is(err, storage.ErrOffset) {
		t.Fatalf("offset mismatch error = %v", err)
	}
	if _, err := engine.Finalize(t.Context(), created.ID, expected); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("incomplete Finalize() error = %v", err)
	}

	resumed, err := engine.Resume(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Offset != int64(len(partial)) || resumed.Metadata["filename"] != "orders.csv" {
		t.Fatalf("Resume() = %#v", resumed)
	}
	if _, err := engine.WriteChunk(t.Context(), created.ID, resumed.Offset, bytes.NewReader(body[len(partial):])); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Finalize(t.Context(), created.ID, expected)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Finalize(t.Context(), created.ID, expected)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("idempotent Finalize() = %#v, %#v", first, second)
	}
	if err := engine.Abort(t.Context(), created.ID); err != nil {
		t.Fatalf("cleanup finalized tus staging: %v", err)
	}
	for _, suffix := range []string{"", ".info"} {
		if _, statErr := os.Stat(filepath.Join(uploadRoot, created.ID+suffix)); !os.IsNotExist(statErr) {
			t.Fatalf("finalized tus staging %q remains: %v", suffix, statErr)
		}
	}
	reader, err := blobs.Open(t.Context(), expected.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("finalized content = %q", got)
	}
}

func TestEngineRecreatedResumePreservesPartialOffsetAndFinalizes(t *testing.T) {
	engine, blobs, uploadRoot := newEngine(t)
	body := []byte("resumable upload content")
	expected := blobFor(body)
	metadata := map[string]string{"filename": "orders.csv"}
	created, err := engine.Create(t.Context(), storage.CreateUpload{
		ID: "upload-recreated", Size: int64(len(body)), Metadata: metadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	partial := body[:9]
	written, err := engine.WriteChunk(t.Context(), created.ID, 0, bytes.NewReader(partial))
	if err != nil {
		t.Fatal(err)
	}
	if written.Offset != int64(len(partial)) {
		t.Fatalf("WriteChunk() offset = %d, want %d", written.Offset, len(partial))
	}

	// A new engine over the same file store must recover the persisted upload state.
	restarted, err := managedtus.New(uploadRoot, blobs)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := restarted.Resume(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Resume() after engine recreation = %v", err)
	}
	if resumed.Offset != int64(len(partial)) || resumed.Size != int64(len(body)) || resumed.Metadata["filename"] != metadata["filename"] {
		t.Fatalf("Resume() after engine recreation = %#v", resumed)
	}
	if _, err := restarted.WriteChunk(t.Context(), resumed.ID, resumed.Offset, bytes.NewReader(body[len(partial):])); err != nil {
		t.Fatalf("WriteChunk() after engine recreation = %v", err)
	}
	finalized, err := restarted.Finalize(t.Context(), created.ID, expected)
	if err != nil {
		t.Fatalf("Finalize() after engine recreation = %v", err)
	}
	reader, err := blobs.Open(t.Context(), finalized.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read = %v, close = %v", readErr, closeErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("finalized content = %q, want %q", got, body)
	}
	if err := restarted.Abort(t.Context(), created.ID); err != nil {
		t.Fatalf("cleanup finalized upload = %v", err)
	}
	for _, suffix := range []string{"", ".info"} {
		if _, statErr := os.Stat(filepath.Join(uploadRoot, created.ID+suffix)); !os.IsNotExist(statErr) {
			t.Fatalf("finalized staging %q remains: %v", suffix, statErr)
		}
	}
}

func TestEngineRejectsInvalidRequestsAndAbortIsIdempotent(t *testing.T) {
	engine, _, _ := newEngine(t)
	if _, err := engine.Create(t.Context(), storage.CreateUpload{ID: "../escape", Size: 1}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("unsafe ID error = %v", err)
	}
	if _, err := engine.Create(t.Context(), storage.CreateUpload{Size: -1}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("negative size error = %v", err)
	}
	created, err := engine.Create(t.Context(), storage.CreateUpload{Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Abort(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := engine.Abort(t.Context(), created.ID); err != nil {
		t.Fatalf("idempotent Abort() = %v", err)
	}
	if _, err := engine.Resume(t.Context(), created.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Resume() after Abort() error = %v", err)
	}
}

func TestEngineStartupRecoversLocksLeftByContainerPIDReuse(t *testing.T) {
	engine, blobs, uploadRoot := newEngine(t)
	if _, err := engine.Create(t.Context(), storage.CreateUpload{ID: "upload-1", Size: 1}); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"upload-1.lock":        strconv.Itoa(os.Getpid()) + "\n",
		"upload-1.stop":        "",
		"upload-1.lock.orphan": strconv.Itoa(os.Getpid()) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(uploadRoot, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	restarted, err := managedtus.New(uploadRoot, blobs)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := restarted.Resume(ctx, "upload-1"); err != nil {
		t.Fatalf("Resume() after container PID reuse = %v", err)
	}
	for _, name := range []string{"upload-1.lock", "upload-1.stop", "upload-1.lock.orphan"} {
		if _, err := os.Stat(filepath.Join(uploadRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("interrupted lock artifact %q survived restart: %v", name, err)
		}
	}
}

func TestHTTPHandlerCompletesTusUploadIntoBlobStore(t *testing.T) {
	engine, blobs, _ := newEngine(t)
	handler, err := engine.HTTPHandler(managedtus.HTTPConfig{BasePath: "/uploads/", MaxSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("over tus")
	expected := blobFor(body)

	create := httptest.NewRequest(http.MethodPost, "http://example.test/uploads/", nil)
	create.Header.Set("Tus-Resumable", "1.0.0")
	create.Header.Set("Upload-Length", strconv.Itoa(len(body)))
	create.Header.Set("Upload-Metadata", "sha256 "+base64.StdEncoding.EncodeToString([]byte(expected.SHA256)))
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	location, err := url.Parse(createResponse.Header().Get("Location"))
	if err != nil || location.Path == "" {
		t.Fatalf("Location = %q, error = %v", createResponse.Header().Get("Location"), err)
	}

	patch := httptest.NewRequest(http.MethodPatch, "http://example.test"+location.Path, bytes.NewReader(body))
	patch.Header.Set("Tus-Resumable", "1.0.0")
	patch.Header.Set("Upload-Offset", "0")
	patch.Header.Set("Content-Type", "application/offset+octet-stream")
	patchResponse := httptest.NewRecorder()
	handler.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, body = %s", patchResponse.Code, patchResponse.Body.String())
	}
	if _, err := blobs.Stat(t.Context(), expected.SHA256); err != nil {
		t.Fatalf("completed tus upload was not finalized: %v", err)
	}
	uploadID := filepath.Base(location.Path)
	if err := engine.Abort(t.Context(), uploadID); err != nil {
		t.Fatalf("Abort() after HTTP completion = %v", err)
	}
	if err := engine.Abort(t.Context(), uploadID); err != nil {
		t.Fatalf("idempotent Abort() after HTTP completion = %v", err)
	}
}

func TestHTTPHandlerRejectsMissingOrInvalidDigestMetadata(t *testing.T) {
	engine, _, _ := newEngine(t)
	handler, err := engine.HTTPHandler(managedtus.HTTPConfig{BasePath: "/uploads/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []string{"", "sha256 " + base64.StdEncoding.EncodeToString([]byte("invalid"))} {
		request := httptest.NewRequest(http.MethodPost, "http://example.test/uploads/", nil)
		request.Header.Set("Tus-Resumable", "1.0.0")
		request.Header.Set("Upload-Length", "1")
		request.Header.Set("Upload-Metadata", metadata)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code < 400 {
			t.Fatalf("metadata %q status = %d", metadata, response.Code)
		}
	}
}

func newEngine(t *testing.T) (*managedtus.Engine, *filesystem.Store, string) {
	t.Helper()
	root := t.TempDir()
	blobs, err := filesystem.New(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	uploadRoot := filepath.Join(root, "uploads")
	engine, err := managedtus.New(uploadRoot, blobs)
	if err != nil {
		t.Fatal(err)
	}
	return engine, blobs, uploadRoot
}

func blobFor(body []byte) storage.Blob {
	sum := sha256.Sum256(body)
	return storage.Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}
