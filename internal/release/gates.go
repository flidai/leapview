package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

type GateOutcome string

const (
	GateSuccess     GateOutcome = "success"
	GateWarning     GateOutcome = "warning"
	GateBlocking    GateOutcome = "blocking"
	GateUnavailable GateOutcome = "unavailable"
	GateEmpty       GateOutcome = "empty"
	GateTimeout     GateOutcome = "timeout"
)

// GateObservationFailure identifies why live source observation did not
// produce evidence. It is deliberately closed so persisted gate evidence can
// distinguish a query bound from an unavailable source without carrying raw
// database errors or connection details.
type GateObservationFailure string

const (
	GateObservationFailureUnavailable GateObservationFailure = "unavailable"
	GateObservationFailureTimeout     GateObservationFailure = "timeout"
	GateObservationFailureBounds      GateObservationFailure = "bounds"
)

type GateBounds struct {
	MaxRows    int64 `json:"maxRows"`
	MaxQueries int   `json:"maxQueries"`
	MaxMillis  int64 `json:"maxMillis"`
}

type GateCheckEvidence struct {
	Identity          string      `json:"identity"`
	Kind              string      `json:"kind"`
	ResourceID        string      `json:"resourceId"`
	Outcome           GateOutcome `json:"outcome"`
	Severity          string      `json:"severity,omitempty"`
	ObservedRows      int64       `json:"observedRows,omitempty"`
	Queries           int         `json:"queries"`
	ObservationDigest string      `json:"observationDigest,omitempty"`
}

type GateSourceEvidence struct {
	ID                 string                       `json:"id"`
	Mode               string                       `json:"mode"`
	SourceDigest       string                       `json:"sourceDigest"`
	ObservedSchema     []semanticmodel.ColumnSchema `json:"observedSchema,omitempty"`
	SchemaDigest       string                       `json:"schemaDigest,omitempty"`
	SchemaOutcome      GateOutcome                  `json:"schemaOutcome"`
	SchemaFailure      GateObservationFailure       `json:"schemaFailure,omitempty"`
	ObservationQueries int                          `json:"observationQueries"`
	ObservationRows    int64                        `json:"observationRows"`
	ObservationMillis  int64                        `json:"observationMillis"`
	FreshnessOutcome   GateOutcome                  `json:"freshnessOutcome,omitempty"`
	FreshnessFailure   GateObservationFailure       `json:"freshnessFailure,omitempty"`
	FreshnessAgeMillis int64                        `json:"freshnessAgeMillis,omitempty"`
	ObservationDigest  string                       `json:"observationDigest,omitempty"`
	ObservedAt         time.Time                    `json:"observedAt,omitempty"`
}

// GateEvidence is the normalized, non-secret candidate gate record. It is
// carried through sealed candidate resolved-input JSON and release provenance.
type GateEvidence struct {
	Version           int                  `json:"version"`
	CandidateID       string               `json:"candidateId"`
	SourceDigest      string               `json:"sourceDigest"`
	BindingGeneration string               `json:"bindingGeneration"`
	RuntimeVersion    string               `json:"runtimeVersion"`
	DuckDBVersion     string               `json:"duckdbVersion"`
	Bounds            GateBounds           `json:"bounds"`
	Outcome           GateOutcome          `json:"outcome"`
	EvaluatedAt       time.Time            `json:"evaluatedAt"`
	Queries           int                  `json:"queries"`
	ObservedRows      int64                `json:"observedRows"`
	DurationMillis    int64                `json:"durationMillis"`
	DurationExceeded  bool                 `json:"durationExceeded,omitempty"`
	QueriesExceeded   bool                 `json:"queriesExceeded,omitempty"`
	RowsExceeded      bool                 `json:"rowsExceeded,omitempty"`
	Sources           []GateSourceEvidence `json:"sources,omitempty"`
	Checks            []GateCheckEvidence  `json:"checks,omitempty"`
	Digest            string               `json:"digest"`
}

func (e GateEvidence) Validate() error {
	if e.Version < 1 || e.CandidateID == "" || e.CandidateID != strings.TrimSpace(e.CandidateID) || e.SourceDigest != strings.TrimSpace(e.SourceDigest) || e.BindingGeneration != strings.TrimSpace(e.BindingGeneration) || e.RuntimeVersion != strings.TrimSpace(e.RuntimeVersion) || e.DuckDBVersion != strings.TrimSpace(e.DuckDBVersion) || e.EvaluatedAt.IsZero() || !e.EvaluatedAt.Equal(e.EvaluatedAt.UTC()) {
		return fmt.Errorf("invalid gate evidence identity")
	}
	if e.SourceDigest == "" || platformdigest.ValidateSHA256Identity(e.SourceDigest) != nil || e.BindingGeneration == "" || platformdigest.ValidateSHA256Identity(e.BindingGeneration) != nil || e.RuntimeVersion == "" || e.DuckDBVersion == "" || e.Bounds.MaxRows <= 0 || e.Bounds.MaxQueries <= 0 || e.Bounds.MaxMillis <= 0 || e.Outcome == "" || e.Queries < 0 || e.ObservedRows < 0 || e.DurationMillis < 0 || (!e.QueriesExceeded && e.Queries > e.Bounds.MaxQueries) || (!e.RowsExceeded && e.ObservedRows > e.Bounds.MaxRows) || (!e.DurationExceeded && e.DurationMillis > e.Bounds.MaxMillis) {
		return fmt.Errorf("invalid gate evidence bounds or identity")
	}
	seenSources := make(map[string]struct{}, len(e.Sources))
	accountedQueries := 0
	accountedRows := int64(0)
	for _, source := range e.Sources {
		if source.ID == "" || source.ID != strings.TrimSpace(source.ID) || source.Mode == "" || source.SourceDigest == "" || platformdigest.ValidateSHA256Identity(source.SourceDigest) != nil {
			return fmt.Errorf("invalid gate source evidence")
		}
		if _, duplicate := seenSources[source.ID]; duplicate {
			return fmt.Errorf("duplicate gate source evidence %q", source.ID)
		}
		seenSources[source.ID] = struct{}{}
		if source.ObservationQueries < 0 || source.ObservationRows < 0 || source.ObservationMillis < 0 || source.ObservationQueries > e.Bounds.MaxQueries || source.ObservationRows > e.Bounds.MaxRows || source.ObservationMillis > e.Bounds.MaxMillis {
			return fmt.Errorf("invalid gate source observation bounds")
		}
		accountedQueries += source.ObservationQueries
		accountedRows += source.ObservationRows
		switch source.Mode {
		case "inferred", "compatible", "strict":
		default:
			return fmt.Errorf("invalid gate source mode %q", source.Mode)
		}
		switch source.SchemaOutcome {
		case GateSuccess, GateWarning, GateBlocking, GateUnavailable, GateEmpty, GateTimeout:
		default:
			return fmt.Errorf("invalid gate schema outcome %q", source.SchemaOutcome)
		}
		switch source.SchemaFailure {
		case "", GateObservationFailureUnavailable, GateObservationFailureTimeout, GateObservationFailureBounds:
		default:
			return fmt.Errorf("invalid gate schema observation failure %q", source.SchemaFailure)
		}
		if err := validateObservationFailureOutcome(source.SchemaFailure, source.SchemaOutcome, "schema"); err != nil {
			return err
		}
		if source.SchemaDigest != "" && platformdigest.ValidateSHA256Identity(source.SchemaDigest) != nil {
			return fmt.Errorf("invalid gate schema digest")
		}
		if source.ObservationDigest != "" && platformdigest.ValidateSHA256Identity(source.ObservationDigest) != nil {
			return fmt.Errorf("invalid gate source observation digest")
		}
		seenColumns := make(map[string]struct{}, len(source.ObservedSchema))
		for _, column := range source.ObservedSchema {
			if column.Name == "" || column.Name != strings.TrimSpace(column.Name) {
				return fmt.Errorf("invalid observed source column")
			}
			if _, duplicate := seenColumns[column.Name]; duplicate {
				return fmt.Errorf("duplicate observed source column %q", column.Name)
			}
			seenColumns[column.Name] = struct{}{}
		}
		if !source.ObservedAt.IsZero() && !source.ObservedAt.Equal(source.ObservedAt.UTC()) {
			return fmt.Errorf("invalid gate source observation time")
		}
		switch source.FreshnessOutcome {
		case "", GateSuccess, GateWarning, GateBlocking, GateUnavailable, GateEmpty, GateTimeout:
		default:
			return fmt.Errorf("invalid gate freshness outcome %q", source.FreshnessOutcome)
		}
		switch source.FreshnessFailure {
		case "", GateObservationFailureUnavailable, GateObservationFailureTimeout, GateObservationFailureBounds:
		default:
			return fmt.Errorf("invalid gate freshness observation failure %q", source.FreshnessFailure)
		}
		if err := validateObservationFailureOutcome(source.FreshnessFailure, source.FreshnessOutcome, "freshness"); err != nil {
			return err
		}
	}
	seenChecks := make(map[string]struct{}, len(e.Checks))
	for _, check := range e.Checks {
		if check.Identity == "" || check.Kind == "" || check.ResourceID == "" || check.Queries < 0 || check.ObservedRows < 0 || (!e.QueriesExceeded && check.Queries > e.Bounds.MaxQueries) || (!e.RowsExceeded && check.ObservedRows > e.Bounds.MaxRows) {
			return fmt.Errorf("invalid gate check evidence")
		}
		accountedQueries += check.Queries
		accountedRows += check.ObservedRows
		if _, duplicate := seenChecks[check.Identity]; duplicate {
			return fmt.Errorf("duplicate gate check evidence %q", check.Identity)
		}
		seenChecks[check.Identity] = struct{}{}
		switch check.Kind {
		case "non_null", "unique", "accepted_values", "relationship", "row_count":
		default:
			return fmt.Errorf("invalid gate check kind %q", check.Kind)
		}
		if check.Severity != "warning" && check.Severity != "error" {
			return fmt.Errorf("invalid gate check severity %q", check.Severity)
		}
		switch check.Outcome {
		case GateSuccess, GateWarning, GateBlocking, GateUnavailable, GateEmpty, GateTimeout:
		default:
			return fmt.Errorf("invalid gate outcome %q", check.Outcome)
		}
	}
	if accountedQueries > e.Queries || accountedRows > e.ObservedRows {
		return fmt.Errorf("gate aggregate totals do not cover component evidence")
	}
	componentOutcome := GateSuccess
	for _, source := range e.Sources {
		componentOutcome = maxOutcome(componentOutcome, source.SchemaOutcome)
		componentOutcome = maxOutcome(componentOutcome, source.FreshnessOutcome)
	}
	for _, check := range e.Checks {
		componentOutcome = maxOutcome(componentOutcome, check.Outcome)
	}
	if e.DurationExceeded {
		componentOutcome = maxOutcome(componentOutcome, GateTimeout)
	} else if e.QueriesExceeded || e.RowsExceeded {
		componentOutcome = maxOutcome(componentOutcome, GateUnavailable)
	}
	if e.Outcome != componentOutcome {
		return fmt.Errorf("gate aggregate outcome does not match component outcomes")
	}
	if e.Digest != "" {
		canonical := e
		canonical.Digest = ""
		encoded, _ := json.Marshal(canonical)
		if platformdigest.ValidateSHA256Identity(e.Digest) != nil || e.Digest != platformDigest(encoded) {
			return fmt.Errorf("gate evidence digest mismatch")
		}
	}
	return nil
}

func validateObservationFailureOutcome(failure GateObservationFailure, outcome GateOutcome, dimension string) error {
	switch failure {
	case "":
		return nil
	case GateObservationFailureTimeout:
		if outcome != GateTimeout {
			return fmt.Errorf("%s observation timeout must have timeout outcome", dimension)
		}
	case GateObservationFailureUnavailable, GateObservationFailureBounds:
		if outcome != GateUnavailable {
			return fmt.Errorf("%s observation %q must have unavailable outcome", dimension, failure)
		}
	}
	return nil
}

func maxOutcome(current, candidate GateOutcome) GateOutcome {
	rank := func(value GateOutcome) int {
		switch value {
		case GateTimeout:
			return 6
		case GateBlocking:
			return 5
		case GateUnavailable:
			return 4
		case GateEmpty:
			return 3
		case GateWarning:
			return 2
		case GateSuccess:
			return 1
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

func (e GateEvidence) Canonical() (GateEvidence, error) {
	e.Sources = append([]GateSourceEvidence(nil), e.Sources...)
	e.Checks = append([]GateCheckEvidence(nil), e.Checks...)
	for i := range e.Sources {
		e.Sources[i].ObservedSchema = append([]semanticmodel.ColumnSchema(nil), e.Sources[i].ObservedSchema...)
		sort.Slice(e.Sources[i].ObservedSchema, func(a, b int) bool {
			if e.Sources[i].ObservedSchema[a].Name != e.Sources[i].ObservedSchema[b].Name {
				return e.Sources[i].ObservedSchema[a].Name < e.Sources[i].ObservedSchema[b].Name
			}
			return e.Sources[i].ObservedSchema[a].Ordinal < e.Sources[i].ObservedSchema[b].Ordinal
		})
	}
	sort.Slice(e.Sources, func(i, j int) bool { return e.Sources[i].ID < e.Sources[j].ID })
	sort.Slice(e.Checks, func(i, j int) bool { return e.Checks[i].Identity < e.Checks[j].Identity })
	e.Digest = ""
	if err := e.Validate(); err != nil {
		return GateEvidence{}, err
	}
	encoded, _ := json.Marshal(e)
	e.Digest = platformDigest(encoded)
	return e, nil
}

func platformDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
