package sqlite

// This file contains the SQLite control-plane adapter for plan-driven delivery.
// The adapter intentionally uses short, explicit transactions for every
// transition. DuckLake catalogs and object-store bytes are outside these
// transactions; SQLite records the durable evidence and CAS fences only.

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func deliveryTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullableDeliveryTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return deliveryTime(t)
}

func nullableString(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: deliveryTime(t), Valid: true}
}

func presentString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func parseDeliveryTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func parseNullableDeliveryTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return parseDeliveryTime(value.String)
}

func deliveryConflict(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, deployment.ErrDeliveryConflict) || errors.Is(err, deployment.ErrDeliveryStale) || errors.Is(err, deployment.ErrDeliveryTransition) || errors.Is(err, deployment.ErrDeliveryInvalid) {
		return err
	}
	return err
}
