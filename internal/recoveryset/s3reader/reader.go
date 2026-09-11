// Package s3reader provides the production-owned exact-version reader used by
// successor recovery evidence.  It accepts an already configured AWS client;
// locators never select credentials, endpoints, or buckets at read time.
package s3reader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	platformtypednil "github.com/flidai/leapview/internal/platform/typednil"
	postgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/recoveryset/successor"
)

const (
	defaultMaxObjectBytes int64 = successor.MaxDocumentBytes
	zeroSHA256                  = "0000000000000000000000000000000000000000000000000000000000000000"
)

// Errors returned by Reader are deliberately static.  Provider diagnostics
// can contain signed URLs, credentials, or other sensitive request material,
// so they are classified and discarded at this boundary.
var (
	ErrInvalid             = errors.New("exact-version S3 reader input is invalid")
	ErrMissingVersion      = errors.New("exact-version S3 object version is missing")
	ErrAccessDenied        = errors.New("exact-version S3 object access denied")
	ErrProfileMismatch     = errors.New("exact-version S3 storage profile does not match")
	ErrIntegrity           = errors.New("exact-version S3 object integrity check failed")
	ErrProviderUnavailable = errors.New("exact-version S3 provider is unavailable")
)

// Client is intentionally the narrow AWS surface needed for an exact read.
// The caller owns credentials, endpoint selection, retry policy, and client
// construction.  In particular, Reader does not construct a client from a
// persisted locator.
type Client interface {
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

// ProfileTuple is the trusted, construction-time identity of one S3 storage
// profile. All seven values are compared exactly with a locator before any
// provider call. Endpoint is an identity value only; requests are sent by the
// preconfigured Client.
type ProfileTuple struct {
	StorageProfileID       string
	StorageProfileRevision int64
	AccountIdentity        string
	Endpoint               string
	Region                 string
	Bucket                 string
	Namespace              string
}

// Config controls Reader construction.  Profile is copied into the Reader,
// preventing later mutation of caller-owned profile memory from changing the
// authority used for reads.
type Config struct {
	Profile        ProfileTuple
	MaxObjectBytes int64
}

// Reader implements postgres.ExactVersionReader.  It returns an in-memory
// ReadCloser containing bytes that have passed the exact metadata, size, and
// SHA-256 checks.  Returning an independent reader also means the provider
// response body is always closed before ReadExact returns.
type Reader struct {
	client         Client
	profile        ProfileTuple
	maxObjectBytes int64
}

var _ postgres.ExactVersionReader = (*Reader)(nil)

// New constructs an exact-version reader with a trusted profile tuple and an
// already configured narrow S3 client.
func New(client Client, config Config) (*Reader, error) {
	if nilClient(client) {
		return nil, ErrInvalid
	}
	if err := config.Profile.validate(); err != nil {
		return nil, err
	}
	maxObjectBytes := config.MaxObjectBytes
	if maxObjectBytes == 0 {
		maxObjectBytes = defaultMaxObjectBytes
	}
	if maxObjectBytes < 1 || maxObjectBytes > defaultMaxObjectBytes {
		return nil, ErrInvalid
	}
	return &Reader{client: client, profile: config.Profile, maxObjectBytes: maxObjectBytes}, nil
}

func (p ProfileTuple) validate() error {
	// Reuse the frozen locator owner for every target-identity rule. The
	// synthetic payload fields are fixed valid values and do not become part of
	// the configured profile or any provider request.
	probe := postgres.ValidatedLocator{
		Backend:                "s3",
		StorageProfileID:       p.StorageProfileID,
		StorageProfileRevision: p.StorageProfileRevision,
		AccountIdentity:        p.AccountIdentity,
		Endpoint:               p.Endpoint,
		Region:                 p.Region,
		Bucket:                 p.Bucket,
		Namespace:              p.Namespace,
		Key:                    p.Namespace + "/recovery-set/v3/sha256/" + zeroSHA256,
		VersionID:              "profile-validation",
		PayloadFamily:          postgres.PayloadFamilySet,
		PayloadVersion:         successor.RecoverySetVersion,
		PayloadDigest:          "sha256:" + zeroSHA256,
		PayloadSHA256:          zeroSHA256,
		PayloadSize:            2,
	}
	if err := probe.Validate(); err != nil {
		return ErrInvalid
	}
	return nil
}

func (p ProfileTuple) matches(locator postgres.ValidatedLocator) bool {
	return p.StorageProfileID == locator.StorageProfileID &&
		p.StorageProfileRevision == locator.StorageProfileRevision &&
		p.AccountIdentity == locator.AccountIdentity &&
		p.Endpoint == locator.Endpoint &&
		p.Region == locator.Region &&
		p.Bucket == locator.Bucket &&
		p.Namespace == locator.Namespace
}

// ReadExact reads the exact object version described by locator.  It never
// retries without VersionId and never uses the latest object as a fallback.
func (r *Reader) ReadExact(ctx context.Context, locator postgres.ValidatedLocator) (io.ReadCloser, error) {
	if r == nil || nilClient(r.client) || ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// This comparison must precede locator validation and all provider I/O:
	// persisted locator data cannot redirect a trusted client to another
	// profile, bucket, account, endpoint, or region.
	if !r.profile.matches(locator) {
		return nil, ErrProfileMismatch
	}
	if locator.VersionID == "" || strings.EqualFold(locator.VersionID, "latest") || strings.EqualFold(locator.VersionID, "null") {
		return nil, ErrMissingVersion
	}
	if err := locator.Validate(); err != nil {
		return nil, ErrInvalid
	}
	if locator.PayloadSize < 0 || locator.PayloadSize > r.maxObjectBytes {
		return nil, ErrIntegrity
	}

	bucket := aws.String(r.profile.Bucket)
	key := aws.String(locator.Key)
	version := aws.String(locator.VersionID)
	head, err := r.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key, VersionId: version})
	if err != nil {
		return nil, classifyProviderError(ctx, err)
	}
	if head == nil {
		return nil, ErrIntegrity
	}
	if aws.ToBool(head.DeleteMarker) {
		return nil, ErrMissingVersion
	}
	if head.VersionId == nil || aws.ToString(head.VersionId) == "" || aws.ToString(head.VersionId) != locator.VersionID || head.ContentLength == nil || *head.ContentLength < 0 || *head.ContentLength != locator.PayloadSize {
		return nil, ErrIntegrity
	}

	object, err := r.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, VersionId: version})
	if err != nil {
		if object != nil && object.Body != nil {
			_ = object.Body.Close()
		}
		return nil, classifyProviderError(ctx, err)
	}
	if object == nil {
		return nil, ErrProviderUnavailable
	}
	if aws.ToBool(object.DeleteMarker) {
		if object.Body != nil {
			_ = object.Body.Close()
		}
		return nil, ErrMissingVersion
	}
	if object.Body == nil {
		return nil, ErrIntegrity
	}
	body := object.Body
	if object.VersionId == nil || aws.ToString(object.VersionId) == "" || aws.ToString(object.VersionId) != locator.VersionID || object.ContentLength == nil || *object.ContentLength < 0 || *object.ContentLength != locator.PayloadSize {
		_ = body.Close()
		return nil, ErrIntegrity
	}

	// Read one byte beyond the expected size.  This both bounds allocation and
	// rejects providers that return more bytes than the exact object metadata.
	limited := io.LimitReader(body, locator.PayloadSize+1)
	raw, readErr := io.ReadAll(limited)
	closeErr := body.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errors.Is(readErr, context.Canceled) || errors.Is(closeErr, context.Canceled) {
		return nil, context.Canceled
	}
	if errors.Is(readErr, context.DeadlineExceeded) || errors.Is(closeErr, context.DeadlineExceeded) {
		return nil, context.DeadlineExceeded
	}
	if readErr != nil || closeErr != nil {
		return nil, ErrProviderUnavailable
	}
	if int64(len(raw)) != locator.PayloadSize {
		return nil, ErrIntegrity
	}

	wantHash := strings.TrimPrefix(locator.PayloadSHA256, "sha256:")
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != wantHash {
		return nil, ErrIntegrity
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func nilClient(client Client) bool {
	return platformtypednil.IsNil(client)
}

func classifyProviderError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	if status := providerHTTPStatus(err); status == 401 || status == 403 {
		return ErrAccessDenied
	}
	if status := providerHTTPStatus(err); status == 404 {
		return ErrMissingVersion
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "nosuchversion", "invalidversionid", "nosuchkey", "nosuchobject", "notfound":
			return ErrMissingVersion
		case "accessdenied", "invalidaccesskeyid", "signaturedoesnotmatch", "expiredtoken", "requestexpired", "authorizationheadermalformed", "invalidsecurity":
			return ErrAccessDenied
		}
	}
	return ErrProviderUnavailable
}

func providerHTTPStatus(err error) int {
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		return responseErr.HTTPStatusCode()
	}
	return 0
}
