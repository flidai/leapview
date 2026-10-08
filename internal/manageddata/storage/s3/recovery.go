package s3

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/manageddata/storage"
)

// RecoverMultipart retries a durable completion using provider-reported ETags.
// Recovery requires the entire reserved shape to match: the original selected
// subset is not durable, so unused reservations cannot safely be guessed away.
// Completed objects retain the ordinary full-byte and provider-version checks.
func (s *Store) RecoverMultipart(ctx context.Context, upload storage.MultipartUpload, reserved []storage.MultipartPartRequest) (storage.Blob, error) {
	if ctx == nil {
		return storage.Blob{}, storage.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return storage.Blob{}, err
	}
	if err := s.validateMultipart(upload); err != nil {
		return storage.Blob{}, err
	}
	expected := storage.Blob{SHA256: upload.SHA256, Size: upload.Size}
	if existing, err := s.verify(ctx, expected); err == nil {
		_ = s.AbortMultipart(ctx, upload)
		if s.profile != nil {
			return storage.Blob{}, fmt.Errorf("%w: existing multipart object has no authoritative completion response", storage.ErrProviderVersion)
		}
		return existing, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return storage.Blob{}, err
	}
	if upload.Existing || strings.TrimSpace(upload.UploadID) == "" {
		return storage.Blob{}, storage.ErrNotFound
	}
	ordered, err := recoveryReservations(upload.Size, reserved)
	if err != nil {
		return storage.Blob{}, err
	}
	lister, ok := s.client.(interface {
		ListParts(context.Context, *awss3.ListPartsInput, ...func(*awss3.Options)) (*awss3.ListPartsOutput, error)
	})
	if !ok {
		return storage.Blob{}, fmt.Errorf("%w: multipart part listing is unavailable", storage.ErrBackend)
	}
	completed := make([]storage.CompletedMultipartPart, 0, len(ordered))
	var marker *string
	for {
		if err := ctx.Err(); err != nil {
			return storage.Blob{}, err
		}
		page, err := lister.ListParts(ctx, &awss3.ListPartsInput{
			Bucket: pointer(s.bucket), Key: pointer(upload.Key), UploadId: pointer(upload.UploadID),
			PartNumberMarker: marker, MaxParts: pointer(int32(1000)),
		})
		if err != nil {
			if isCode(err, "NoSuchUpload") {
				if s.profile != nil {
					return storage.Blob{}, fmt.Errorf("%w: unresolved multipart completion has no authoritative response", storage.ErrProviderVersion)
				}
				return s.verify(ctx, expected)
			}
			return storage.Blob{}, sanitizeError(ctx, "list S3 multipart parts", err)
		}
		if page == nil || page.Bucket == nil || *page.Bucket != s.bucket || page.Key == nil || *page.Key != upload.Key ||
			page.UploadId == nil || *page.UploadId != upload.UploadID || page.IsTruncated == nil || len(page.Parts) > 1000 {
			return storage.Blob{}, fmt.Errorf("%w: multipart part listing identity or bounds are invalid", storage.ErrIntegrity)
		}
		for _, part := range page.Parts {
			if len(completed) >= len(ordered) || part.PartNumber == nil || part.Size == nil || part.ETag == nil ||
				strings.TrimSpace(*part.ETag) == "" || strings.ContainsAny(*part.ETag, "\x00\r\n") || len(*part.ETag) > 1024 {
				return storage.Blob{}, fmt.Errorf("%w: multipart provider part is invalid", storage.ErrIntegrity)
			}
			reservation := ordered[len(completed)]
			if *part.PartNumber != reservation.Number || *part.Size != reservation.Size {
				return storage.Blob{}, fmt.Errorf("%w: multipart provider part does not match its reservation", storage.ErrIntegrity)
			}
			if reservation.SHA256 != "" {
				checksum, _ := checksumBase64(reservation.SHA256)
				if part.ChecksumSHA256 == nil || *part.ChecksumSHA256 != checksum {
					return storage.Blob{}, fmt.Errorf("%w: multipart provider checksum does not match its reservation", storage.ErrIntegrity)
				}
			}
			completed = append(completed, storage.CompletedMultipartPart{Number: reservation.Number, ETag: *part.ETag, SHA256: reservation.SHA256})
		}
		if !*page.IsTruncated {
			if len(completed) != len(ordered) {
				return storage.Blob{}, fmt.Errorf("%w: multipart provider parts are incomplete", storage.ErrIntegrity)
			}
			break
		}
		if len(page.Parts) == 0 || len(completed) >= len(ordered) || page.NextPartNumberMarker == nil ||
			*page.NextPartNumberMarker != strconv.FormatInt(int64(completed[len(completed)-1].Number), 10) {
			return storage.Blob{}, fmt.Errorf("%w: multipart part pagination does not advance canonically", storage.ErrIntegrity)
		}
		marker = page.NextPartNumberMarker
	}
	if err := ctx.Err(); err != nil {
		return storage.Blob{}, err
	}
	return s.CompleteMultipart(ctx, upload, completed)
}

func recoveryReservations(size int64, reserved []storage.MultipartPartRequest) ([]storage.MultipartPartRequest, error) {
	if len(reserved) == 0 || len(reserved) > 10_000 {
		return nil, fmt.Errorf("%w: multipart recovery requires bounded reservations", storage.ErrInvalid)
	}
	ordered := append([]storage.MultipartPartRequest(nil), reserved...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	var total int64
	for index, part := range ordered {
		if part.Number < 1 || part.Number > 10_000 || part.Size <= 0 || part.Size > 5*1024*1024*1024 ||
			index > 0 && ordered[index-1].Number == part.Number || index < len(ordered)-1 && part.Size < 5*1024*1024 || total > size-part.Size {
			return nil, fmt.Errorf("%w: multipart recovery reservations are invalid or ambiguous", storage.ErrInvalid)
		}
		if part.SHA256 != "" {
			if err := storage.ValidateSHA256(part.SHA256); err != nil {
				return nil, err
			}
		}
		total += part.Size
	}
	if total != size {
		return nil, fmt.Errorf("%w: multipart recovery reservations do not match the complete object", storage.ErrInvalid)
	}
	return ordered, nil
}
