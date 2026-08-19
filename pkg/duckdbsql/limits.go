package duckdbsql

import "fmt"

// Limits bounds work performed by the parser and serialized-AST decoder.
// Zero values are replaced with DefaultLimits by New and Parse.
type Limits struct {
	MaxSQLBytes   int
	MaxJSONBytes  int
	MaxDepth      int
	MaxNodes      int
	MaxArrayItems int
}

func DefaultLimits() Limits {
	return Limits{MaxSQLBytes: 1 << 20, MaxJSONBytes: 8 << 20, MaxDepth: 256, MaxNodes: 250_000, MaxArrayItems: 100_000}
}

func (l Limits) normalized() (Limits, error) {
	d := DefaultLimits()
	if l.MaxSQLBytes == 0 {
		l.MaxSQLBytes = d.MaxSQLBytes
	}
	if l.MaxJSONBytes == 0 {
		l.MaxJSONBytes = d.MaxJSONBytes
	}
	if l.MaxDepth == 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxNodes == 0 {
		l.MaxNodes = d.MaxNodes
	}
	if l.MaxArrayItems == 0 {
		l.MaxArrayItems = d.MaxArrayItems
	}
	if l.MaxSQLBytes < 1 || l.MaxJSONBytes < 1 || l.MaxDepth < 1 || l.MaxNodes < 1 || l.MaxArrayItems < 1 {
		return Limits{}, fmt.Errorf("duckdbsql limits must be positive")
	}
	return l, nil
}
