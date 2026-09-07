package ducklake

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidCatalogVersion = errors.New("DuckLake catalog version is invalid")

// CanonicalCatalogVersion normalizes the version stored by DuckLake in its
// global catalog options. DuckLake renders major formats with a zero minor
// component (for example 1.0), while platform tuples use the equivalent major
// identity (ducklake-catalog:v1). Non-zero minor/pre-release versions remain
// distinct so a future format change cannot be admitted as the same contract.
func CanonicalCatalogVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrInvalidCatalogVersion
	}
	if index := strings.IndexByte(value, ':'); index >= 0 {
		prefix := value[:index]
		if prefix != "ducklake" && prefix != "ducklake-catalog" {
			return "", ErrInvalidCatalogVersion
		}
		value = value[index+1:]
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalidCatalogVersion
	}
	if major, found := strings.CutSuffix(value, ".0"); found {
		parsed, err := strconv.ParseInt(major, 10, 64)
		if err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == major {
			return major, nil
		}
	}
	return value, nil
}

// CatalogVersionNumber returns the positive major version stored in delivery
// seal evidence. It accepts DuckLake's equivalent zero-minor presentation but
// rejects a genuinely distinct minor or pre-release format.
func CatalogVersionNumber(value string) (int64, error) {
	canonical, err := CanonicalCatalogVersion(value)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(canonical, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != canonical {
		return 0, ErrInvalidCatalogVersion
	}
	return parsed, nil
}
