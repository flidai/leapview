package observationstore

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/recoveryset/observation"
)

func (s *Store) verifySource(ctx context.Context, object observation.ProviderObject, required time.Time) (observation.Protection, error) {
	return s.verifySourceAt(ctx, object, time.Time{}, s.now(), required)
}

func (s *Store) verifySourceAt(ctx context.Context, object observation.ProviderObject, recordedUntil, now, required time.Time) (observation.Protection, error) {
	if object.Endpoint != s.endpoint || object.Region != s.region || object.Bucket != s.sourceBucket {
		return observation.Protection{}, fmt.Errorf("%w: source object is outside configured provider namespace", ErrInvalid)
	}
	if object.Size > s.maxSourceBytes {
		return observation.Protection{}, fmt.Errorf("%w: source object exceeds bounded verification size", ErrInvalid)
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: ptr(s.sourceBucket), Key: ptr(object.Key), VersionId: ptr(object.VersionID)})
	if err != nil {
		return observation.Protection{}, backendError(ctx, "head source object", err)
	}
	if head == nil || head.ContentLength == nil || *head.ContentLength != object.Size || stringValue(head.VersionId) != object.VersionID {
		return observation.Protection{}, fmt.Errorf("%w: source object version, size, or identity differs", ErrIntegrity)
	}
	retention, err := s.retention(ctx, s.sourceBucket, object.Key, object.VersionID)
	if err != nil {
		return observation.Protection{}, err
	}
	if retention.Mode != types.ObjectLockRetentionModeCompliance || retention.RetainUntilDate == nil || retention.RetainUntilDate.Before(required) || (!now.IsZero() && !retention.RetainUntilDate.After(now)) {
		return observation.Protection{}, fmt.Errorf("%w: source object is not COMPLIANCE-retained through required horizon", ErrRetention)
	}
	get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: ptr(s.sourceBucket), Key: ptr(object.Key), VersionId: ptr(object.VersionID)})
	if err != nil {
		return observation.Protection{}, backendError(ctx, "get source object", err)
	}
	if get != nil && get.Body != nil && (stringValue(get.VersionId) != object.VersionID || get.ContentLength == nil || *get.ContentLength != object.Size) {
		_ = get.Body.Close()
		return observation.Protection{}, fmt.Errorf("%w: source object response metadata differs", ErrIntegrity)
	}
	if get == nil || get.Body == nil {
		return observation.Protection{}, fmt.Errorf("%w: source object body is missing", ErrBackend)
	}
	actualSize, actualHash, readErr := hashBody(get.Body, object.Size)
	closeErr := get.Body.Close()
	if readErr != nil || closeErr != nil {
		return observation.Protection{}, fmt.Errorf("%w: source object body could not be verified", ErrIntegrity)
	}
	if actualSize != object.Size || actualHash != object.SHA256 {
		return observation.Protection{}, fmt.Errorf("%w: source object bytes differ from captured hash and size", ErrIntegrity)
	}
	until := retention.RetainUntilDate.UTC()
	if !recordedUntil.IsZero() && until.Before(recordedUntil) {
		return observation.Protection{}, fmt.Errorf("%w: provider source protection is shorter than recorded protection", ErrIntegrity)
	}
	return observation.Protection{Object: object, Mode: string(retention.Mode), RetainUntil: until}, nil
}

func (s *Store) retention(ctx context.Context, bucket, key, version string) (types.ObjectLockRetention, error) {
	result, err := s.client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: ptr(bucket), Key: ptr(key), VersionId: ptr(version)})
	if err != nil {
		// Providers commonly report an object without Object Lock as either a
		// 400 InvalidRequest or a 404 configuration/method error.  Both mean
		// this exact source version is not admissible evidence; preserve the
		// retention classification without exposing provider error text.
		switch apiCode(err) {
		case "BadRequest", "InvalidRequest", "InvalidRequestException", "MethodNotAllowed", "NoSuchObjectLockConfiguration", "NotImplemented":
			return types.ObjectLockRetention{}, fmt.Errorf("%w: object retention is unavailable", ErrRetention)
		}
		return types.ObjectLockRetention{}, backendError(ctx, "get object retention", err)
	}
	if result == nil || result.Retention == nil {
		return types.ObjectLockRetention{}, fmt.Errorf("%w: object retention response is incomplete", ErrRetention)
	}
	return *result.Retention, nil
}
