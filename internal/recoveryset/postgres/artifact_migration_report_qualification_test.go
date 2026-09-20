//go:build fai518qualification && fai518artifactqualification

package postgres_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestFAI518ArtifactMigrationReportRejectsRevisionAndIdentityMismatch(t *testing.T) {
	set, evidence, capability := artifactBoundaryFixture(t)
	bound, err := set.bind(evidence, capability)
	if err != nil {
		t.Fatal(err)
	}
	bound.ObservedRevision = 20
	valid := realArtifactReport{Migration: bound, MigrationRevision: 20, Image: bound.CandidateImage, SourceRevision: bound.CandidateSourceRevision, AdmissionDigest: bound.CandidateAdmissionDigest, PredecessorAdmissionDigest: bound.PredecessorAdmissionDigest}
	for _, name := range []string{"revision 021", "observed revision 021", "candidate admission", "predecessor admission", "candidate image", "source revision"} {
		t.Run(name, func(t *testing.T) {
			report := valid
			switch name {
			case "revision 021":
				report.MigrationRevision = 21
			case "observed revision 021":
				report.Migration.ObservedRevision = 21
			case "candidate admission":
				report.AdmissionDigest = productionDigest('f')
			case "predecessor admission":
				report.PredecessorAdmissionDigest = productionDigest('f')
			case "candidate image":
				report.Image += "0"
			case "source revision":
				report.SourceRevision += "0"
			}
			dir := t.TempDir()
			if err := writeRealArtifactReport(dir, report); err == nil {
				t.Fatal("invalid success report accepted")
			}
			if _, err := os.Stat(dir + "/transition-report.json"); !os.IsNotExist(err) {
				t.Fatalf("rejected report was persisted: %v", err)
			}
		})
	}
	dir := t.TempDir()
	if err := writeRealArtifactReport(dir, valid); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(dir + "/transition-report.json")
	if err != nil {
		t.Fatal(err)
	}
	var readback realArtifactReport
	if err := json.Unmarshal(contents, &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Migration != bound || readback.MigrationRevision != 20 {
		t.Fatal("persisted report lost observed revision/artifact binding")
	}
}
