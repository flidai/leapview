package ducklake

import "github.com/flidai/leapview/internal/analytics/ducklake/metadata"

var ErrInvalidCatalogVersion = metadata.ErrInvalidCatalogVersion

func CanonicalCatalogVersion(value string) (string, error) {
	return metadata.CanonicalCatalogVersion(value)
}
func CatalogVersionNumber(value string) (int64, error) { return metadata.CatalogVersionNumber(value) }
