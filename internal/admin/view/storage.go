package view

type AdminStorageData struct {
	Status             string
	TotalDataSizeBytes int64
	TotalDataSizeLabel string
	TableCount         int
	DataFileCount      int
	Tables             []AdminStorageTable
}

type AdminStorageTable struct {
	Schema        string
	Name          string
	Type          string
	TableID       int64
	TableUUID     string
	DuckLakePath  string
	BeginSnapshot int64
	EndSnapshot   int64
	RowCount      int64
	RowCountLabel string
	ColumnCount   int
	FileCount     int
	SizeBytes     int64
	SizeLabel     string
	Files         []AdminStorageFile
}

type AdminStorageFile struct {
	ID               int64
	Path             string
	Format           string
	RecordCount      int64
	RecordCountLabel string
	SizeBytes        int64
	SizeLabel        string
	BeginSnapshot    int64
	EndSnapshot      int64
}
