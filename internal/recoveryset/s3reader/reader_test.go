package s3reader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	postgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/recoveryset/successor"
)

const testProfileID = "22222222-2222-4222-8222-222222222222"

type fakeClient struct {
	headOutput *awss3.HeadObjectOutput
	headErr    error
	getOutput  *awss3.GetObjectOutput
	getErr     error
	headCalls  int
	getCalls   int
	lastHead   *awss3.HeadObjectInput
	lastGet    *awss3.GetObjectInput
}

func (f *fakeClient) HeadObject(_ context.Context, input *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	f.headCalls++
	f.lastHead = input
	return f.headOutput, f.headErr
}

func (f *fakeClient) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	f.getCalls++
	f.lastGet = input
	return f.getOutput, f.getErr
}

type closeTrackingBody struct {
	io.Reader
	closed bool
	err    error
	reads  int
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return b.err
}

func (b *closeTrackingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func TestFAI520ExactReaderUsesExactProfileAndVersionOnBothRequests(t *testing.T) {
	body := []byte(`{"profile":"exact"}`)
	client, reader, locator := newReader(t, body)
	result, err := reader.ReadExact(t.Context(), locator)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("exact read returned nil body")
	}
	defer result.Close()
	if client.headCalls != 1 || client.getCalls != 1 {
		t.Fatalf("calls = HEAD %d, GET %d, want one each", client.headCalls, client.getCalls)
	}
	if got := aws.ToString(client.lastHead.Bucket); got != locator.Bucket {
		t.Fatalf("HEAD bucket = %q, want %q", got, locator.Bucket)
	}
	if got := aws.ToString(client.lastHead.Key); got != locator.Key {
		t.Fatalf("HEAD key = %q, want %q", got, locator.Key)
	}
	if got := aws.ToString(client.lastHead.VersionId); got != locator.VersionID {
		t.Fatalf("HEAD version = %q, want %q", got, locator.VersionID)
	}
	if got := aws.ToString(client.lastGet.Bucket); got != locator.Bucket {
		t.Fatalf("GET bucket = %q, want %q", got, locator.Bucket)
	}
	if got := aws.ToString(client.lastGet.Key); got != locator.Key {
		t.Fatalf("GET key = %q, want %q", got, locator.Key)
	}
	if got := aws.ToString(client.lastGet.VersionId); got != locator.VersionID {
		t.Fatalf("GET version = %q, want %q", got, locator.VersionID)
	}
}

func TestFAI520ExactReaderRejectsDeleteMarkers(t *testing.T) {
	t.Run("head", func(t *testing.T) {
		client, reader, locator := newReader(t, []byte(`{"ok":true}`))
		client.headOutput.DeleteMarker = aws.Bool(true)
		if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrMissingVersion) {
			t.Fatalf("error = %v, want missing version", err)
		}
		if client.getCalls != 0 {
			t.Fatalf("GET calls = %d, want zero", client.getCalls)
		}
	})

	t.Run("get", func(t *testing.T) {
		client, reader, locator := newReader(t, []byte(`{"ok":true}`))
		tracking := &closeTrackingBody{Reader: bytes.NewReader([]byte(`{"ok":true}`))}
		client.getOutput.Body = tracking
		client.getOutput.DeleteMarker = aws.Bool(true)
		if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrMissingVersion) {
			t.Fatalf("error = %v, want missing version", err)
		}
		if !tracking.closed {
			t.Fatal("provider response body was not closed")
		}
	})
}

func TestFAI520ExactReaderProfileMismatchMakesZeroProviderCalls(t *testing.T) {
	client, reader, locator := newReader(t, []byte(`{"ok":true}`))
	locator.Endpoint = "https://other.example.test"
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrProfileMismatch) {
		t.Fatalf("error = %v, want profile mismatch", err)
	}
	if client.headCalls != 0 || client.getCalls != 0 {
		t.Fatalf("calls = HEAD %d, GET %d, want zero", client.headCalls, client.getCalls)
	}
}

func TestFAI520ExactReaderRejectsInvalidConstruction(t *testing.T) {
	_, _, locator := newReader(t, []byte(`{"ok":true}`))
	valid := Config{Profile: ProfileTuple{
		StorageProfileID: locator.StorageProfileID, StorageProfileRevision: locator.StorageProfileRevision,
		AccountIdentity: locator.AccountIdentity, Endpoint: locator.Endpoint, Region: locator.Region,
		Bucket: locator.Bucket, Namespace: locator.Namespace,
	}}
	client := &fakeClient{}
	if _, err := New(nil, valid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil client error = %v, want invalid", err)
	}
	var typedNil *fakeClient
	if _, err := New(typedNil, valid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed nil client error = %v, want invalid", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "profile id", mutate: func(config *Config) { config.Profile.StorageProfileID = "not-a-uuid" }},
		{name: "profile revision", mutate: func(config *Config) { config.Profile.StorageProfileRevision = 0 }},
		{name: "credential endpoint", mutate: func(config *Config) { config.Profile.Endpoint = "https://user:secret@objects.example.test" }},
		{name: "bucket", mutate: func(config *Config) { config.Profile.Bucket = "UPPERCASE" }},
		{name: "namespace", mutate: func(config *Config) { config.Profile.Namespace = "../evidence" }},
		{name: "maximum bytes", mutate: func(config *Config) { config.MaxObjectBytes = int64(successor.MaxDocumentBytes) + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := valid
			tc.mutate(&config)
			if _, err := New(client, config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
		})
	}
}

func TestFAI520ExactReaderRejectsMalformedLocatorBeforeIO(t *testing.T) {
	client, reader, locator := newReader(t, []byte(`{"ok":true}`))
	locator.Key = "evidence/not-the-content-addressed-key"
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
	if client.headCalls != 0 || client.getCalls != 0 {
		t.Fatalf("calls = HEAD %d, GET %d, want zero", client.headCalls, client.getCalls)
	}
}

func TestFAI520ExactReaderRejectsEveryProfileTupleMismatchBeforeIO(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*postgres.ValidatedLocator)
	}{
		{name: "profile id", mutate: func(locator *postgres.ValidatedLocator) {
			locator.StorageProfileID = "33333333-3333-4333-8333-333333333333"
		}},
		{name: "profile revision", mutate: func(locator *postgres.ValidatedLocator) { locator.StorageProfileRevision++ }},
		{name: "account", mutate: func(locator *postgres.ValidatedLocator) { locator.AccountIdentity = "other-account" }},
		{name: "endpoint", mutate: func(locator *postgres.ValidatedLocator) { locator.Endpoint = "https://other.example.test" }},
		{name: "region", mutate: func(locator *postgres.ValidatedLocator) { locator.Region = "us-west-2" }},
		{name: "bucket", mutate: func(locator *postgres.ValidatedLocator) { locator.Bucket = "other-bucket" }},
		{name: "namespace", mutate: func(locator *postgres.ValidatedLocator) { locator.Namespace = "other" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			client, reader, locator := newReader(t, []byte(`{"ok":true}`))
			tc.mutate(&locator)
			if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrProfileMismatch) {
				t.Fatalf("error = %v, want profile mismatch", err)
			}
			if client.headCalls != 0 || client.getCalls != 0 {
				t.Fatalf("calls = HEAD %d, GET %d, want zero", client.headCalls, client.getCalls)
			}
		})
	}
}

func TestFAI520ExactReaderDoesNotFallbackForMissingVersion(t *testing.T) {
	client, reader, locator := newReader(t, []byte(`{"ok":true}`))
	locator.VersionID = ""
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrMissingVersion) {
		t.Fatalf("error = %v, want missing version", err)
	}
	if client.headCalls != 0 || client.getCalls != 0 {
		t.Fatalf("calls = HEAD %d, GET %d, want zero", client.headCalls, client.getCalls)
	}

	for _, version := range []string{"latest", "LATEST", "null", "NULL"} {
		client.headCalls, client.getCalls = 0, 0
		locator.VersionID = version
		if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrMissingVersion) {
			t.Errorf("version %q error = %v, want missing version", version, err)
		}
		if client.headCalls != 0 || client.getCalls != 0 {
			t.Errorf("version %q calls = HEAD %d, GET %d, want zero", version, client.headCalls, client.getCalls)
		}
	}
}

func TestFAI520ExactReaderSanitizesProviderErrors(t *testing.T) {
	secret := "signed-url-and-secret-key"
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{name: "missing version", err: &smithy.GenericAPIError{Code: "NoSuchVersion", Message: secret}, want: ErrMissingVersion},
		{name: "access denied", err: &smithy.GenericAPIError{Code: "AccessDenied", Message: secret}, want: ErrAccessDenied},
		{name: "provider unavailable", err: errors.New(secret), want: ErrProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, reader, locator := newReader(t, []byte(`{"ok":true}`))
			client.headErr = tc.err
			_, err := reader.ReadExact(t.Context(), locator)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked provider diagnostic: %q", err)
			}
		})
	}
}

func TestFAI520ExactReaderRetrySucceedsAfterProviderRepair(t *testing.T) {
	want := []byte(`{"ok":true}`)
	client, reader, locator := newReader(t, want)
	client.headErr = errors.New("temporary provider outage")
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("first error = %v, want provider unavailable", err)
	}

	client.headErr = nil
	result, err := reader.ReadExact(t.Context(), locator)
	if err != nil {
		t.Fatalf("repaired read: %v", err)
	}
	defer result.Close()
	got, err := io.ReadAll(result)
	if err != nil {
		t.Fatalf("read repaired result: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("repaired bytes = %q, want %q", got, want)
	}
}

func TestFAI520ExactReaderClosesProviderBodyReturnedWithError(t *testing.T) {
	client, reader, locator := newReader(t, []byte(`{"ok":true}`))
	secret := "provider-body-error-secret"
	body := &closeTrackingBody{Reader: bytes.NewReader([]byte(secret))}
	client.getOutput.Body = body
	client.getErr = errors.New(secret)
	_, err := reader.ReadExact(t.Context(), locator)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("error = %v, want provider unavailable", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked provider diagnostic: %q", err)
	}
	if !body.closed {
		t.Fatal("provider response body was not closed")
	}
}

func TestFAI520ExactReaderClosesBodyOnSuccessAndIntegrityFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		getBody []byte
		wantErr bool
	}{
		{name: "success", body: []byte(`{"ok":true}`), getBody: []byte(`{"ok":true}`)},
		{name: "hash mismatch", body: []byte(`{"ok":true}`), getBody: []byte(`{"ok":null}`), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, reader, locator := newReader(t, tc.body)
			tracking := &closeTrackingBody{Reader: bytes.NewReader(tc.getBody)}
			client.getOutput.Body = tracking
			if _, err := reader.ReadExact(t.Context(), locator); (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tracking.closed {
				t.Fatal("provider response body was not closed")
			}
		})
	}
}

func TestFAI520ExactReaderSanitizesBodyReadAndCloseErrors(t *testing.T) {
	secret := "provider body secret"
	for _, tc := range []struct {
		name      string
		readError error
		closeErr  error
	}{
		{name: "read", readError: errors.New(secret)},
		{name: "close", closeErr: errors.New(secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, reader, locator := newReader(t, []byte(`{"ok":true}`))
			tracking := &closeTrackingBody{Reader: errorReader{err: tc.readError}, err: tc.closeErr}
			client.getOutput.Body = tracking
			_, err := reader.ReadExact(t.Context(), locator)
			if !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("error = %v, want provider unavailable", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked provider diagnostic: %q", err)
			}
			if !tracking.closed {
				t.Fatal("provider response body was not closed")
			}
		})
	}
}

func TestFAI520ExactReaderRejectsMissingOrWrongReturnedVersionAndSize(t *testing.T) {
	client, reader, locator := newReader(t, []byte(`{"ok":true}`))
	client.getOutput.VersionId = nil
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing GET version error = %v, want integrity", err)
	}

	client.getOutput.VersionId = aws.String("different-version")
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong GET version error = %v, want integrity", err)
	}

	client.getOutput.VersionId = aws.String(locator.VersionID)
	client.getOutput.ContentLength = nil
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing GET size error = %v, want integrity", err)
	}

	client.getOutput.ContentLength = aws.Int64(locator.PayloadSize + 1)
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong GET size error = %v, want integrity", err)
	}

	client.headOutput.VersionId = nil
	client.getOutput.ContentLength = aws.Int64(locator.PayloadSize)
	if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing HEAD version error = %v, want integrity", err)
	}
}

func TestFAI520ExactReaderRejectsShortAndOversizedBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "short", body: []byte(`{"ok":}`)},
		{name: "oversized", body: []byte(`{"ok":true} trailing`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, reader, locator := newReader(t, []byte(`{"ok":true}`))
			client.getOutput.Body = io.NopCloser(bytes.NewReader(tc.body))
			if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error = %v, want integrity", err)
			}
		})
	}
}

func TestFAI520ExactReaderClosesBodyBeforeRejectingGETMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*awss3.GetObjectOutput)
	}{
		{name: "missing version", mutate: func(output *awss3.GetObjectOutput) { output.VersionId = nil }},
		{name: "wrong version", mutate: func(output *awss3.GetObjectOutput) { output.VersionId = aws.String("other-version") }},
		{name: "missing content length", mutate: func(output *awss3.GetObjectOutput) { output.ContentLength = nil }},
		{name: "wrong content length", mutate: func(output *awss3.GetObjectOutput) { output.ContentLength = aws.Int64(1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, reader, locator := newReader(t, []byte(`{"ok":true}`))
			tracking := &closeTrackingBody{Reader: bytes.NewReader([]byte(`{"ok":true}`))}
			client.getOutput.Body = tracking
			tc.mutate(client.getOutput)
			if _, err := reader.ReadExact(t.Context(), locator); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error = %v, want integrity", err)
			}
			if !tracking.closed {
				t.Fatal("provider response body was not closed")
			}
			if tracking.reads != 0 {
				t.Fatalf("provider body reads = %d, want zero", tracking.reads)
			}
		})
	}
}

func TestFAI520ExactReaderPreservesContextCancellation(t *testing.T) {
	client, reader, locator := newReader(t, []byte(`{"ok":true}`))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.ReadExact(ctx, locator); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if client.headCalls != 0 || client.getCalls != 0 {
		t.Fatalf("calls = HEAD %d, GET %d, want zero", client.headCalls, client.getCalls)
	}
}

func newReader(t *testing.T, body []byte) (*fakeClient, *Reader, postgres.ValidatedLocator) {
	t.Helper()
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	locator := postgres.ValidatedLocator{
		Backend:                "s3",
		StorageProfileID:       testProfileID,
		StorageProfileRevision: 3,
		AccountIdentity:        "account-1",
		Endpoint:               "https://objects.example.test",
		Region:                 "us-east-1",
		Bucket:                 "evidence-bucket",
		Namespace:              "evidence",
		Key:                    fmt.Sprintf("evidence/managed-observation-manifest/v2/sha256/%s", hash),
		VersionID:              "opaque-version-1",
		PayloadFamily:          postgres.PayloadFamilyManifest,
		PayloadVersion:         2,
		PayloadDigest:          "sha256:" + hash,
		PayloadSHA256:          hash,
		PayloadSize:            int64(len(body)),
	}
	client := &fakeClient{
		headOutput: &awss3.HeadObjectOutput{VersionId: aws.String(locator.VersionID), ContentLength: aws.Int64(locator.PayloadSize)},
		getOutput:  &awss3.GetObjectOutput{VersionId: aws.String(locator.VersionID), ContentLength: aws.Int64(locator.PayloadSize), Body: io.NopCloser(bytes.NewReader(body))},
	}
	reader, err := New(client, Config{Profile: ProfileTuple{StorageProfileID: locator.StorageProfileID, StorageProfileRevision: locator.StorageProfileRevision, AccountIdentity: locator.AccountIdentity, Endpoint: locator.Endpoint, Region: locator.Region, Bucket: locator.Bucket, Namespace: locator.Namespace}})
	if err != nil {
		t.Fatal(err)
	}
	return client, reader, locator
}
