package postgres

import (
	"embed"
	"strings"
)

func credentialSchemaSQL(files embed.FS) string {
	var result strings.Builder
	for _, name := range []string{"schema.sql", "request_schema.sql", "rotation_schema.sql", "first_source_schema.sql"} {
		data, err := files.ReadFile(name)
		if err != nil {
			panic(err)
		}
		result.Write(data)
		result.WriteByte('\n')
	}
	return result.String()
}
