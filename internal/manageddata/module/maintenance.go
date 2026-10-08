package module

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
)

type Maintenance struct {
	uploads   *control.Service
	multipart *s3multipart.Service
	uploadTTL time.Duration
	collector *maintenance.BlobCollector
	runtime   *maintenance.RuntimeViewCollector
}

func (m Maintenance) ExpireUploads(ctx context.Context) (control.ExpireResult, error) {
	if ctx == nil {
		return control.ExpireResult{}, control.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return control.ExpireResult{}, err
	}
	result, err := m.uploads.ExpireUploads(ctx)
	var failures []error
	record := func(stage string, err error) bool {
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", stage, err))
		}
		if canceled := ctx.Err(); canceled != nil {
			if !errors.Is(err, canceled) {
				failures = append(failures, canceled)
			}
			return true
		}
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	}
	if record("expire uploads", err) {
		return result, errors.Join(failures...)
	}
	// Blob reachability does not include completing multipart uploads whose
	// parent is terminal. Keep expiry/recovery success as a prerequisite for
	// physical blob collection until that lifecycle is independently fenced.
	blobsEligible := err == nil
	if blobsEligible && m.multipart != nil {
		_, err = m.multipart.RecoverOrphaned(ctx, time.Now().UTC().Add(-m.uploadTTL), 100)
		blobsEligible = err == nil
		if record("recover multipart uploads", err) {
			return result, errors.Join(failures...)
		}
	}
	if blobsEligible && m.collector != nil {
		_, err = m.collector.Run(ctx)
		if record("collect blobs", err) {
			return result, errors.Join(failures...)
		}
	}
	// Runtime views have their own exact candidate and live-lease guards, so an
	// unrelated upload/provider/blob error must not starve their collection.
	if m.runtime != nil {
		_, err = m.runtime.Run(ctx)
		record("collect runtime views", err)
	}
	return result, errors.Join(failures...)
}
