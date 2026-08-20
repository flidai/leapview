package module

import "github.com/flidai/leapview/internal/analytics/catalogstats"

// CatalogTableStatistic is the credential-free physical table rollup exposed
// by an active analytical runtime.
type CatalogTableStatistic = catalogstats.Table

// CatalogStatisticsReader is the capability boundary implemented by runtimes
// that can inspect their exact serving snapshot.
type CatalogStatisticsReader = catalogstats.Reader
