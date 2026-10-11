package metadata

// BaseTable identifies one visible, non-temporary DuckLake base table. It is
// intentionally value-only so callers cannot obtain a mutable SQL handle.
type BaseTable struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

type FileKind string

const (
	DataFile   FileKind = "data"
	DeleteFile FileKind = "delete"
)

type CatalogFileSet struct {
	CatalogID   string
	DataFiles   []string
	DeleteFiles []string
}
