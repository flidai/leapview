package saved

import (
	"errors"

	"github.com/flidai/leapview/internal/analytics/dataquery"
)

// ExportFormat and ExportLimits are the browser-facing export contract. The
// analytics module owns encoding; project transport never imports its encoder.
type ExportFormat string

const (
	ExportCSV             ExportFormat = "csv"
	ExportParquet         ExportFormat = "parquet"
	ExportDefaultMaxRows               = 10_000
	ExportDefaultMaxBytes int64        = 32 << 20
	ExportMaximumMaxRows               = 10_000
	ExportMaximumMaxBytes int64        = 32 << 20
)

type ExportLimits = dataquery.ResultLimits

var (
	ErrExportInvalidFormat  = errors.New("unsupported exploration export format")
	ErrExportInvalidRequest = errors.New("invalid exploration export request")
	ErrExportPartial        = errors.New("exploration result is incomplete")
	ErrExportCanceled       = errors.New("exploration export canceled")
)
