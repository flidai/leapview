// Package querymap owns the dashboard report-definition to governed data-query
// conversion boundary.
//
// Dashboard production callers must use this package for fields, filters (and
// nested filter groups), spatial filters, and sorts. Keeping this boundary in
// one package prevents execution surfaces from acquiring subtly different
// query semantics. The report package aliases the input types here so the
// mapper can also be used by report's own data service without an import cycle.
package querymap
