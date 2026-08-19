package duckdbsql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type qualificationCorpus struct {
	Corpus        string              `json:"corpus"`
	CorpusVersion int                 `json:"corpus_version"`
	Engine        qualificationEngine `json:"engine"`
	Cases         []qualificationCase `json:"cases"`
}

type qualificationEngine struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	SourceCommit string `json:"source_commit"`
}

type qualificationCase struct {
	ID        string                   `json:"id"`
	SQL       string                   `json:"sql"`
	Source    qualificationSource      `json:"source"`
	Admission string                   `json:"admission"`
	Expect    qualificationExpectation `json:"expect"`
}

type qualificationSource struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	License    string `json:"license"`
}

type qualificationExpectation struct {
	Parse         string   `json:"parse"`
	ErrorKind     string   `json:"error_kind"`
	Statement     string   `json:"statement"`
	Statements    *int     `json:"statements"`
	Relations     *int     `json:"relations"`
	RelationNames []string `json:"relation_names"`
	Functions     *int     `json:"functions"`
	FunctionNames []string `json:"function_names"`
	Columns       *int     `json:"columns"`
	CTEs          []string `json:"ctes"`
}

const duckDBWebSourceCommit = "0f4f64ac3c12c139edfd57947ceed142178c9419"

func loadQualificationCorpus(t *testing.T) qualificationCorpus {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "testdata", "corpus.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read qualification corpus: %v", err)
	}
	var corpus qualificationCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode qualification corpus: %v", err)
	}
	if corpus.Corpus != "duckdbsql-phase-1-select" || corpus.CorpusVersion != 1 {
		t.Fatalf("unexpected corpus identity: %#v", corpus)
	}
	if corpus.Engine.Name != "duckdb" || corpus.Engine.Version != DuckDBVersion || corpus.Engine.SourceCommit != DuckDBSourceCommit {
		t.Fatalf("corpus engine is not pinned to the package engine: %#v", corpus.Engine)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("qualification corpus is empty")
	}
	seen := map[string]bool{}
	for _, c := range corpus.Cases {
		if c.ID == "" || seen[c.ID] {
			t.Fatalf("corpus case id is empty or duplicated: %q", c.ID)
		}
		seen[c.ID] = true
		if strings.TrimSpace(c.SQL) == "" {
			t.Fatalf("corpus case %q has empty SQL", c.ID)
		}
		if c.Source.Kind == "" || c.Source.Repository == "" || c.Source.Commit == "" || c.Source.Path == "" {
			t.Fatalf("corpus case %q is missing source provenance: %#v", c.ID, c.Source)
		}
		if c.Admission != "not_evaluated" {
			t.Fatalf("corpus case %q must explicitly leave admission policy unevaluated, got %q", c.ID, c.Admission)
		}
		switch c.Source.Kind {
		case "duckdb":
			if c.Source.Commit != DuckDBSourceCommit {
				t.Fatalf("corpus case %q is not pinned to DuckDB commit %s: %s", c.ID, DuckDBSourceCommit, c.Source.Commit)
			}
		case "duckdb-docs":
			if c.Source.Commit != duckDBWebSourceCommit {
				t.Fatalf("corpus case %q is not pinned to DuckDB-web commit %s: %s", c.ID, duckDBWebSourceCommit, c.Source.Commit)
			}
		case "leapsql":
			if c.Source.Commit != "e6c4605252eac2aefe3cf502c9b156edee05a42d" {
				t.Fatalf("corpus case %q has unexpected LeapSQL commit %s", c.ID, c.Source.Commit)
			}
		case "sqlglot":
			if c.Source.Commit != "1ba7b2715dd9aba896edc7dc69fcc41bbb601b90d" {
				t.Fatalf("corpus case %q has unexpected SQLGlot commit %s", c.ID, c.Source.Commit)
			}
		default:
			t.Fatalf("corpus case %q has unrecognized source kind %q", c.ID, c.Source.Kind)
		}
		switch c.Source.License {
		case "MIT", "Apache-2.0":
		default:
			t.Fatalf("corpus case %q has unapproved license %q", c.ID, c.Source.License)
		}
		if c.Expect.Parse != "accept" && c.Expect.Parse != "reject" {
			t.Fatalf("corpus case %q has invalid parse expectation %q", c.ID, c.Expect.Parse)
		}
	}
	return corpus
}

func TestPhase1QualificationCorpus(t *testing.T) {
	corpus := loadQualificationCorpus(t)
	counts := map[string]int{}
	for _, c := range corpus.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			counts[c.Source.Kind+"@"+c.Source.Commit]++
			query, err := Parse(context.Background(), c.SQL)
			if c.Expect.Parse == "reject" {
				if err == nil {
					t.Fatalf("Parse unexpectedly accepted %q", c.SQL)
				}
				if c.Expect.ErrorKind != "" {
					var parseErr *ParseError
					if !errors.As(err, &parseErr) || parseErr.Kind.String() != c.Expect.ErrorKind {
						t.Fatalf("error kind = %v, want %s (error=%v)", parseErr, c.Expect.ErrorKind, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.SQL, err)
			}
			if c.Expect.Statements != nil && len(query.Statements) != *c.Expect.Statements {
				t.Fatalf("statements = %d, want %d", len(query.Statements), *c.Expect.Statements)
			}
			if c.Expect.Statement != "" && len(query.Statements) > 0 && statementKind(query.Statements[0]) != c.Expect.Statement {
				t.Fatalf("statement kind = %s, want %s", statementKind(query.Statements[0]), c.Expect.Statement)
			}
			analysis, err := Analyze(query)
			if err != nil {
				t.Fatalf("Analyze(%q): %v", c.SQL, err)
			}
			if c.Expect.Relations != nil && len(analysis.Relations) != *c.Expect.Relations {
				t.Fatalf("relations = %d, want %d (%#v)", len(analysis.Relations), *c.Expect.Relations, analysis.Relations)
			}
			if c.Expect.Functions != nil && len(analysis.Functions) != *c.Expect.Functions {
				t.Fatalf("functions = %d, want %d (%#v)", len(analysis.Functions), *c.Expect.Functions, analysis.Functions)
			}
			if c.Expect.Columns != nil && len(analysis.Columns) != *c.Expect.Columns {
				t.Fatalf("columns = %d, want %d (%#v)", len(analysis.Columns), *c.Expect.Columns, analysis.Columns)
			}
			if len(c.Expect.RelationNames) > 0 {
				got := make([]string, 0, len(analysis.Relations))
				for _, relation := range analysis.Relations {
					got = append(got, relation.Name)
				}
				if !reflect.DeepEqual(got, c.Expect.RelationNames) {
					t.Fatalf("relation names = %#v, want %#v", got, c.Expect.RelationNames)
				}
			}
			if len(c.Expect.FunctionNames) > 0 {
				got := make([]string, 0, len(analysis.Functions))
				for _, function := range analysis.Functions {
					got = append(got, function.Name)
				}
				if !reflect.DeepEqual(got, c.Expect.FunctionNames) {
					t.Fatalf("function names = %#v, want %#v", got, c.Expect.FunctionNames)
				}
			}
			if len(c.Expect.CTEs) > 0 {
				got := make([]string, 0, len(analysis.CTEs))
				for _, cte := range analysis.CTEs {
					got = append(got, cte.Name)
				}
				if !reflect.DeepEqual(got, c.Expect.CTEs) {
					t.Fatalf("CTEs = %#v, want %#v", got, c.Expect.CTEs)
				}
			}
		})
	}
	keys := make([]string, 0, len(counts))
	for source := range counts {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	var summary []string
	for _, source := range keys {
		summary = append(summary, fmt.Sprintf("%s=%d", source, counts[source]))
	}
	t.Logf("qualified %d corpus cases (%s)", len(corpus.Cases), strings.Join(summary, ", "))
}

func statementKind(statement Statement) string {
	switch statement.(type) {
	case *SelectStatement:
		return "select"
	case *SetOperationStatement:
		return "set_operation"
	default:
		return fmt.Sprintf("%T", statement)
	}
}
