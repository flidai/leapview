package s3_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
)

func TestMultipartRecoveryPreservesProviderPartsAndFullVerification(t *testing.T) {
	for _, mode := range []string{"unversioned", "observed", "missing-write-version", "tampered-body"} {
		t.Run(mode, func(t *testing.T) {
			store, client, upload, reserved := multipartRecoveryFixture(t, mode != "unversioned" && mode != "tampered-body")
			if mode == "missing-write-version" {
				client.omitMultipartVersion = true
			}
			if mode == "tampered-body" {
				client.setMultipartBody(upload.UploadID, bytes.Repeat([]byte("x"), int(upload.Size)))
			}
			blob, err := store.RecoverMultipart(t.Context(), upload, reserved)
			switch mode {
			case "missing-write-version":
				if !errors.Is(err, storage.ErrProviderVersion) || blob.ProviderVersion != nil {
					t.Fatalf("unobserved recovery = %#v, %v", blob, err)
				}
			case "tampered-body":
				if !errors.Is(err, storage.ErrIntegrity) || len(client.deleteBatches) != 1 {
					t.Fatalf("tampered recovery = %#v, %v; deletes=%v", blob, err, client.deleteBatches)
				}
			default:
				if err != nil || blob.SHA256 != upload.SHA256 || blob.Size != upload.Size {
					t.Fatalf("recovery = %#v, %v", blob, err)
				}
				if mode == "observed" && (blob.ProviderVersion == nil || blob.ProviderVersion.VersionID != client.lastMultipartVersion || client.lastGetVersion != client.lastMultipartVersion) {
					t.Fatalf("authoritative recovered observation = %#v, get=%q", blob.ProviderVersion, client.lastGetVersion)
				}
			}
			if client.listPartsCalls != 2 || client.completeCalls != 1 {
				t.Fatalf("provider calls: list=%d complete=%d", client.listPartsCalls, client.completeCalls)
			}
		})
	}
}

func TestMultipartRecoveryRejectsUntrustworthyPartsBeforeCompletion(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*multipartRecoveryClient, *[]storage.MultipartPartRequest)
		want   error
	}{
		{"wrong-bucket", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Bucket = testPointer("other")
		}, storage.ErrIntegrity},
		{"wrong-key", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Key = testPointer("other")
		}, storage.ErrIntegrity},
		{"wrong-upload", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].UploadId = testPointer("other")
		}, storage.ErrIntegrity},
		{"missing-truncation", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) { c.pages[0].IsTruncated = nil }, storage.ErrIntegrity},
		{"nil-page", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) { c.pages[0] = nil }, storage.ErrIntegrity},
		{"empty-truncated-page", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) { c.pages[0].Parts = nil }, storage.ErrIntegrity},
		{"missing-marker", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].NextPartNumberMarker = nil
		}, storage.ErrIntegrity},
		{"noncanonical-marker", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].NextPartNumberMarker = testPointer("01")
		}, storage.ErrIntegrity},
		{"nonadvancing-marker", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].NextPartNumberMarker = testPointer("0")
		}, storage.ErrIntegrity},
		{"oversized-page", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Parts = make([]types.Part, 1001)
		}, storage.ErrIntegrity},
		{"duplicate-part", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[1].Parts[0].PartNumber = testPointer(int32(1))
		}, storage.ErrIntegrity},
		{"wrong-size", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[1].Parts[0].Size = testPointer(int64(2))
		}, storage.ErrIntegrity},
		{"empty-etag", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Parts[0].ETag = testPointer(" ")
		}, storage.ErrIntegrity},
		{"unsafe-etag", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Parts[0].ETag = testPointer("etag\r\n")
		}, storage.ErrIntegrity},
		{"checksum-mismatch", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Parts[0].ChecksumSHA256 = testPointer("wrong")
		}, storage.ErrIntegrity},
		{"missing-checksum", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[0].Parts[0].ChecksumSHA256 = nil
		}, storage.ErrIntegrity},
		{"missing-part", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) { c.pages[1].Parts = nil }, storage.ErrIntegrity},
		{"extra-part", func(c *multipartRecoveryClient, _ *[]storage.MultipartPartRequest) {
			c.pages[1].Parts = append(c.pages[1].Parts, c.pages[1].Parts[0])
		}, storage.ErrIntegrity},
		{"ambiguous-reservations", func(_ *multipartRecoveryClient, r *[]storage.MultipartPartRequest) {
			*r = append(*r, storage.MultipartPartRequest{Number: 9, Size: 1})
		}, storage.ErrInvalid},
		{"duplicate-reservation", func(_ *multipartRecoveryClient, r *[]storage.MultipartPartRequest) { (*r)[1].Number = 1 }, storage.ErrInvalid},
		{"short-nonfinal-reservation", func(_ *multipartRecoveryClient, r *[]storage.MultipartPartRequest) { (*r)[0].Size--; (*r)[1].Size++ }, storage.ErrInvalid},
		{"invalid-reserved-checksum", func(_ *multipartRecoveryClient, r *[]storage.MultipartPartRequest) { (*r)[0].SHA256 = "bad" }, storage.ErrInvalid},
		{"empty-reservations", func(_ *multipartRecoveryClient, r *[]storage.MultipartPartRequest) { *r = nil }, storage.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, client, upload, reserved := multipartRecoveryFixture(t, false)
			tc.mutate(client, &reserved)
			if _, err := store.RecoverMultipart(t.Context(), upload, reserved); !errors.Is(err, tc.want) {
				t.Fatalf("recovery error = %v, want %v", err, tc.want)
			}
			if client.completeCalls != 0 {
				t.Fatalf("untrustworthy parts reached provider completion: %d", client.completeCalls)
			}
			if _, ok := client.multipart[upload.UploadID]; !ok {
				t.Fatal("invalid recovery destroyed retryable provider upload")
			}
		})
	}
}

func TestMultipartRecoveryCancellationAndCapabilityFailClosed(t *testing.T) {
	store, client, upload, reserved := multipartRecoveryFixture(t, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client.afterList = func() { cancel() }
	if _, err := store.RecoverMultipart(ctx, upload, reserved); !errors.Is(err, context.Canceled) || client.listPartsCalls != 1 || client.completeCalls != 0 {
		t.Fatalf("canceled recovery: %v, list=%d complete=%d", err, client.listPartsCalls, client.completeCalls)
	}
	withoutListing := newStore(t, client.fakeClient, &fakePresigner{})
	if _, err := withoutListing.RecoverMultipart(t.Context(), upload, reserved); !errors.Is(err, storage.ErrBackend) || client.completeCalls != 0 {
		t.Fatalf("missing part listing capability: %v", err)
	}
}

func TestMultipartRecoveryCompletedAndNoSuchUploadRemainVersionSafe(t *testing.T) {
	for _, observed := range []bool{false, true} {
		for _, state := range []string{"existing", "completed-during-list", "missing-upload"} {
			t.Run(fmt.Sprintf("observed=%t/%s", observed, state), func(t *testing.T) {
				store, client, upload, reserved := multipartRecoveryFixture(t, observed)
				publish := func() {
					pending := client.multipart[upload.UploadID]
					client.objects[upload.Key] = fakeObject{body: pending.body, metadata: pending.metadata, modified: time.Now(), version: "head-only-version"}
				}
				if state == "existing" {
					publish()
					reserved = nil // No part reconstruction is needed for fully verified existing bytes.
				} else {
					client.listErr = fakeAPIError{code: "NoSuchUpload"}
					if state == "completed-during-list" {
						client.afterList = publish
					}
				}
				blob, err := store.RecoverMultipart(t.Context(), upload, reserved)
				if observed {
					if !errors.Is(err, storage.ErrProviderVersion) || blob.ProviderVersion != nil {
						t.Fatalf("inferred provider version: %#v, %v", blob, err)
					}
				} else if state == "missing-upload" {
					if !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("missing upload error = %v", err)
					}
				} else if err != nil || blob.SHA256 != upload.SHA256 || blob.Size != upload.Size {
					t.Fatalf("existing recovery = %#v, %v", blob, err)
				}
				if client.completeCalls != 0 {
					t.Fatalf("ambiguous completion replayed: %d", client.completeCalls)
				}
			})
		}
	}
}

type multipartRecoveryClient struct {
	*fakeClient
	upload                        storage.MultipartUpload
	reserved                      []storage.MultipartPartRequest
	pages                         []*awss3.ListPartsOutput
	listPartsCalls, completeCalls int
	listErr                       error
	afterList                     func()
}

func (c *multipartRecoveryClient) ListParts(_ context.Context, input *awss3.ListPartsInput, _ ...func(*awss3.Options)) (*awss3.ListPartsOutput, error) {
	if dereference(input.Bucket) != "private-data" || dereference(input.Key) != c.upload.Key || dereference(input.UploadId) != c.upload.UploadID || input.MaxParts == nil || *input.MaxParts != 1000 {
		return nil, errors.New("incorrect bounded provider request")
	}
	index := c.listPartsCalls
	c.listPartsCalls++
	if c.afterList != nil {
		c.afterList()
	}
	if c.listErr != nil {
		return nil, c.listErr
	}
	wantMarker := ""
	if index > 0 {
		wantMarker = "1"
	}
	if dereference(input.PartNumberMarker) != wantMarker || index >= len(c.pages) {
		return nil, errors.New("incorrect or unbounded pagination")
	}
	return c.pages[index], nil
}

func (c *multipartRecoveryClient) CompleteMultipartUpload(ctx context.Context, input *awss3.CompleteMultipartUploadInput, options ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error) {
	c.completeCalls++
	if dereference(input.IfNoneMatch) != "*" || input.MultipartUpload == nil || len(input.MultipartUpload.Parts) != 2 {
		return nil, errors.New("completion lost create-only or exact parts")
	}
	for i, part := range input.MultipartUpload.Parts {
		reserved := c.reserved[i]
		checksum := base64.StdEncoding.EncodeToString(mustDecodeHexForRecovery(reserved.SHA256))
		if part.PartNumber == nil || *part.PartNumber != reserved.Number || dereference(part.ETag) != fmt.Sprintf("real-etag-%d", reserved.Number) || dereference(part.ChecksumSHA256) != checksum {
			return nil, errors.New("completion lost actual ETag or checksum")
		}
	}
	return c.fakeClient.CompleteMultipartUpload(ctx, input, options...)
}

func mustDecodeHexForRecovery(digest string) []byte {
	decoded, err := hex.DecodeString(digest)
	if err != nil {
		panic(err)
	}
	return decoded
}

func multipartRecoveryFixture(t *testing.T, observed bool) (*manageds3.Store, *multipartRecoveryClient, storage.MultipartUpload, []storage.MultipartPartRequest) {
	t.Helper()
	first := bytes.Repeat([]byte("a"), 5*1024*1024)
	last := []byte("b")
	body := append(first, last...)
	reserved := []storage.MultipartPartRequest{{Number: 1, Size: int64(len(first)), SHA256: blobFor(first).SHA256}, {Number: 7, Size: 1, SHA256: blobFor(last).SHA256}}
	client := &multipartRecoveryClient{fakeClient: newFakeClient(), reserved: append([]storage.MultipartPartRequest(nil), reserved...)}
	config := manageds3.Config{Bucket: "private-data", Prefix: "managed"}
	if observed {
		profile := storage.ProviderProfileIdentity{ProfileID: "recovery-test", Implementation: "s3", AccountIdentity: "test-account", Endpoint: "https://s3.example.test", Region: "test-region", Bucket: "private-data", Namespace: "managed"}
		config.ObservationProfile = &profile
	}
	store, err := manageds3.New(client, &fakePresigner{}, config)
	if err != nil {
		t.Fatal(err)
	}
	upload, err := store.CreateMultipart(t.Context(), blobFor(body))
	if err != nil {
		t.Fatal(err)
	}
	client.upload = upload
	client.setMultipartBody(upload.UploadID, body)
	for i, part := range reserved {
		page := &awss3.ListPartsOutput{Bucket: testPointer("private-data"), Key: testPointer(upload.Key), UploadId: testPointer(upload.UploadID), IsTruncated: testPointer(i == 0), Parts: []types.Part{{PartNumber: testPointer(part.Number), Size: testPointer(part.Size), ETag: testPointer(fmt.Sprintf("real-etag-%d", part.Number)), ChecksumSHA256: testPointer(base64.StdEncoding.EncodeToString(mustDecodeHexForRecovery(part.SHA256)))}}}
		if i == 0 {
			page.NextPartNumberMarker = testPointer("1")
		}
		client.pages = append(client.pages, page)
	}
	return store, client, upload, reserved
}
