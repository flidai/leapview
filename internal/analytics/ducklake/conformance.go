package ducklake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

const SharedPoolConformanceVersion = "lea-406/v1"

// SharedPoolConformanceChecks is the admission checklist. A compatibility
// result is never issued from a partial checklist: every item must be supplied
// and pass in the same run.
var SharedPoolConformanceChecks = []string{
	"same_table_private_clone_isolation",
	"different_table_private_clone_isolation",
	"unchanged_file_reference_reuse",
	"new_file_key_disjointness",
	"aborted_write_isolation",
	"normalization_file_union_completeness",
	"cross_catalog_orphan_classification",
	"sealed_catalog_read",
	"safe_close",
}

// ConformanceCheck returns a stable observation of the scenario in addition
// to its pass/fail result. The observation is hashed into admission evidence;
// a bare boolean can never admit a pool.
type ConformanceCheck func(context.Context) ([]byte, error)

type SharedPoolConformance struct {
	Compatibility physicalpool.Compatibility
	Checks        map[string]ConformanceCheck
}

// MarshalSharedPoolEvidence emits the reviewed, machine-readable admission
// artifact consumed by the offline bootstrap command. It revalidates the
// complete named checklist so a partial or hand-authored Evidence value cannot
// cross the operator boundary.
func MarshalSharedPoolEvidence(evidence physicalpool.Evidence) ([]byte, error) {
	conformance := SharedPoolConformance{Compatibility: evidence.Compatibility}
	if err := conformance.ValidateEvidence(evidence); err != nil {
		return nil, err
	}
	return physicalpool.MarshalEvidenceArtifact(evidence)
}

// WriteSharedPoolEvidence writes one complete artifact to an operator-owned
// file or stream. The writer is deliberately generic so CI and the MinIO lane
// can persist exactly the same envelope without storing observations.
func WriteSharedPoolEvidence(w io.Writer, evidence physicalpool.Evidence) error {
	if w == nil {
		return fmt.Errorf("evidence artifact writer is required")
	}
	encoded, err := MarshalSharedPoolEvidence(evidence)
	if err != nil {
		return err
	}
	_, err = w.Write(encoded)
	return err
}

// Run executes the complete versioned admission checklist and returns
// physicalpool.Evidence only when every required check was present and passed.
// Missing checks are errors, not skipped observations.
func (c SharedPoolConformance) Run(ctx context.Context) (physicalpool.Evidence, error) {
	if err := c.Compatibility.Validate(); err != nil {
		return physicalpool.Evidence{}, err
	}
	if c.Checks == nil {
		return physicalpool.Evidence{}, fmt.Errorf("shared-pool conformance checks are required")
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(SharedPoolConformanceChecks))
	for _, name := range SharedPoolConformanceChecks {
		check := c.Checks[name]
		if check == nil {
			return physicalpool.Evidence{}, fmt.Errorf("shared-pool conformance check %q is missing", name)
		}
		observation, err := check(ctx)
		if err != nil {
			return physicalpool.Evidence{}, fmt.Errorf("shared-pool conformance check %q failed: %w", name, err)
		}
		if len(observation) == 0 {
			return physicalpool.Evidence{}, fmt.Errorf("shared-pool conformance check %q produced no observation", name)
		}
		sum := sha256.Sum256(observation)
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	// Keep unexpected checks from silently becoming an unreviewed admission
	// surface. They may be added only by extending the versioned checklist.
	for name := range c.Checks {
		if !containsString(SharedPoolConformanceChecks, name) {
			return physicalpool.Evidence{}, fmt.Errorf("unknown shared-pool conformance check %q", name)
		}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	return physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: c.Compatibility, ConformanceVersion: SharedPoolConformanceVersion, Checks: checks,
	})
}

func (c SharedPoolConformance) ValidateEvidence(evidence physicalpool.Evidence) error {
	if evidence.ConformanceVersion != SharedPoolConformanceVersion {
		return fmt.Errorf("unsupported shared-pool conformance version %q", evidence.ConformanceVersion)
	}
	if !evidence.Compatibility.Equal(c.Compatibility) {
		return fmt.Errorf("shared-pool conformance compatibility tuple mismatch")
	}
	if err := evidence.Verify(); err != nil {
		return err
	}
	got := make([]string, 0, len(evidence.Checks))
	for _, check := range evidence.Checks {
		got = append(got, check.ID)
	}
	sort.Strings(got)
	want := append([]string(nil), SharedPoolConformanceChecks...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		return fmt.Errorf("shared-pool conformance evidence is incomplete")
	}
	return nil
}
