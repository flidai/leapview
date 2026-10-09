package s3multipart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
)

// This is a local S3 protocol boundary with the real AWS SDK and store adapter,
// plus native PostgreSQL intent/parts. It does not qualify a live S3 provider.
func TestCoordinatorRecoversUnfinishedProviderCompletionWithRealETags(t *testing.T) {
	body := append(bytes.Repeat([]byte("a"), int(MinimumPartSize)), 'b')
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	_, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: int64(len(body)), SHA256: digest}})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	boundary := &recoveryS3Boundary{body: body, digest: digest}
	server := httptest.NewServer(boundary)
	t.Cleanup(server.Close)
	client := awss3.New(awss3.Options{
		Region: "test-region", BaseEndpoint: aws.String(server.URL), UsePathStyle: true,
		Credentials:      credentials.NewStaticCredentialsProvider("local-test", "local-secret", ""),
		RetryMaxAttempts: 1, HTTPClient: server.Client(),
	})
	store, err := manageds3.New(client, awss3.NewPresignClient(client), manageds3.Config{Bucket: "private-data", Prefix: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, repo, store)
	created, err := service.Create(ctx, CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "provider-recovery-create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []storage.MultipartPartRequest{{Number: 1, Size: MinimumPartSize}, {Number: 2, Size: 1}} {
		_, err := service.SignPart(ctx, SignPartRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: created.ID, PartNumber: part.Number, Size: part.Size})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = service.Complete(ctx, CompleteRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: created.ID, IdempotencyKey: "provider-recovery-complete",
		Parts: []CompletedPart{{PartNumber: 1, ETag: "\"actual-provider-1\""}, {PartNumber: 2, ETag: "\"actual-provider-2\""}}})
	if !errors.Is(err, control.ErrBackend) {
		t.Fatalf("interrupted provider completion = %v, want observable backend failure", err)
	}
	upload, err := repo.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(created.ID))
	if err != nil || upload.Status != manageddata.S3MultipartStatusCompleting {
		t.Fatalf("durable unfinished completion = %#v, %v", upload, err)
	}
	// Recreate the coordinator. The only durable part information is number,
	// size and optional checksum; ETags must come from the provider.
	restarted := newTestService(t, repo, store)
	result, err := restarted.RecoverOrphaned(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || result.Completed != 1 || result.Failed != 0 {
		t.Fatalf("recover unfinished provider completion = %#v, %v", result, err)
	}
	upload, err = repo.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(created.ID))
	if err != nil || upload.Status != manageddata.S3MultipartStatusCompleted {
		t.Fatalf("durable recovered completion = %#v, %v", upload, err)
	}
	boundary.mu.Lock()
	listCalls, completeCalls, finished := boundary.listCalls, boundary.completeCalls, boundary.finished
	boundary.mu.Unlock()
	if listCalls != 2 || completeCalls != 2 || !finished {
		t.Fatalf("provider recovery calls: list=%d complete=%d finished=%v", listCalls, completeCalls, finished)
	}
	repeated, err := restarted.RecoverOrphaned(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || repeated != (RecoveryResult{}) {
		t.Fatalf("terminal recovery replay = %#v, %v", repeated, err)
	}
	t.Log("unfinished native completion recovered using two provider pages and exact provider ETags")
}

type recoveryS3Boundary struct {
	mu            sync.Mutex
	body          []byte
	digest        string
	listCalls     int
	completeCalls int
	finished      bool
}

func (b *recoveryS3Boundary) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	query := r.URL.Query()
	writeError := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Error><Code>%s</Code><Message>controlled local boundary</Message></Error>", code)
	}
	if r.Method == http.MethodPost && query.Has("uploads") {
		io.WriteString(w, "<InitiateMultipartUploadResult><Bucket>private-data</Bucket><Key>managed</Key><UploadId>local-upload</UploadId></InitiateMultipartUploadResult>")
		return
	}
	if query.Get("uploadId") != "" && query.Get("uploadId") != "local-upload" {
		writeError(http.StatusNotFound, "NoSuchUpload")
		return
	}
	if r.Method == http.MethodGet && query.Get("uploadId") == "local-upload" {
		b.listCalls++
		number := 1
		size := MinimumPartSize
		truncated := true
		if query.Get("part-number-marker") == "1" {
			number, size, truncated = 2, 1, false
		} else if query.Get("part-number-marker") != "" {
			writeError(http.StatusBadRequest, "InvalidArgument")
			return
		}
		fmt.Fprintf(w, "<ListPartsResult><Bucket>private-data</Bucket><Key>%s</Key><UploadId>local-upload</UploadId><PartNumberMarker>%d</PartNumberMarker><NextPartNumberMarker>%d</NextPartNumberMarker><MaxParts>1000</MaxParts><IsTruncated>%t</IsTruncated><Part><PartNumber>%d</PartNumber><ETag>&quot;actual-provider-%d&quot;</ETag><Size>%d</Size></Part></ListPartsResult>",
			r.URL.Path[len("/private-data/"):], number-1, number, truncated, number, number, size)
		return
	}
	if r.Method == http.MethodPost && query.Get("uploadId") == "local-upload" {
		b.completeCalls++
		var completion struct {
			Parts []struct {
				Number int32  `xml:"PartNumber"`
				ETag   string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&completion); err != nil || len(completion.Parts) != 2 ||
			completion.Parts[0].Number != 1 || completion.Parts[0].ETag != "\"actual-provider-1\"" ||
			completion.Parts[1].Number != 2 || completion.Parts[1].ETag != "\"actual-provider-2\"" || r.Header.Get("If-None-Match") != "*" {
			writeError(http.StatusBadRequest, "InvalidPart")
			return
		}
		if b.completeCalls == 1 {
			writeError(http.StatusServiceUnavailable, "ServiceUnavailable")
			return
		}
		b.finished = true
		io.WriteString(w, "<CompleteMultipartUploadResult><Bucket>private-data</Bucket><Key>managed</Key><ETag>&quot;object-etag&quot;</ETag></CompleteMultipartUploadResult>")
		return
	}
	if r.Method == http.MethodDelete && query.Get("uploadId") == "local-upload" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !b.finished {
		writeError(http.StatusNotFound, "NotFound")
		return
	}
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(b.body)))
		w.Header().Set("X-Amz-Meta-Sha256", b.digest)
		if r.Method == http.MethodGet {
			w.Write(b.body)
		}
		return
	}
	writeError(http.StatusBadRequest, "InvalidRequest")
}
