import { duckdb, formatDialect } from 'sql-formatter'

export function formatDuckDBSQL(code: string): string {
  return formatDialect(code, {
    dialect: duckdb,
    keywordCase: 'upper',
  }).trim()
}
