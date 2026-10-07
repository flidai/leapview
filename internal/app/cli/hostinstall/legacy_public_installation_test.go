//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/installationstate"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// This qualified revision predates explicit installation phases. Its original
// target-bound marker must survive recovery unchanged and become phased only
// when the candidate is activated.
func TestNativeUpgradePreservesLegacyPublicMarkerUntilCandidateActivation(t *testing.T) {
	e := nativeEffectsFixture(t)
	e.request.PredecessorImage = "ghcr.io/flidai/leapview@sha256:cae683fbdf86032adf78213b88f83612eb545307791adc1a5e040f9402ba41ff"
	e.request.PredecessorRevision = "77ecf56bec6ee4f9a4018c869c802ee545285583"
	e.id.Predecessor = e.request.PredecessorImage
	e.original.Current = "releases/sha256-" + strings.Split(e.id.Predecessor, "sha256:")[1]
	if err := os.MkdirAll(filepath.Join(e.root, e.original.Current), 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{SchemaVersion: 1, Domain: "demo.leapview.dev", AdminEmail: "admin@example.com",
		Environment: "prod", Image: e.id.Predecessor, TargetID: "retained-provisioned-target", HTTPS: boolPointer(true)}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{e.root, filepath.Join(e.operation, "original-config")} {
		if err := securefs.WritePrivateFileAtomic(filepath.Join(root, installMarkerName), raw); err != nil {
			t.Fatal(err)
		}
		deployment := "LEAPVIEW_IMAGE=" + e.id.Predecessor + "\nCOMPOSE_PROJECT_NAME=leapview-cfo\nCOMPOSE_HTTPS=1\nCADDY_DOMAIN=demo.leapview.dev\n"
		if err := securefs.WritePrivateFileAtomic(filepath.Join(root, "deployment.env"), []byte(deployment)); err != nil {
			t.Fatal(err)
		}
	}
	// Activation follows the already-completed candidate start and migration.
	deployment := "LEAPVIEW_IMAGE=" + e.id.Candidate + "\nCOMPOSE_PROJECT_NAME=leapview-cfo\nCOMPOSE_HTTPS=1\nCADDY_DOMAIN=demo.leapview.dev\n"
	if err := securefs.WritePrivateFileAtomic(filepath.Join(e.root, "deployment.env"), []byte(deployment)); err != nil {
		t.Fatal(err)
	}
	e.execute = func(_ context.Context, args ...string) (string, error) {
		deployment, err := os.ReadFile(filepath.Join(e.root, "deployment.env"))
		if err != nil {
			return "", err
		}
		image, revision, schema := e.id.Predecessor, e.request.PredecessorRevision, e.request.Plan.CurrentSchema
		if strings.Contains(string(deployment), "LEAPVIEW_IMAGE="+e.id.Candidate+"\n") {
			image, revision, schema = e.id.Candidate, e.request.CandidateRevision, e.request.Plan.CandidateSchema
		}
		call := strings.Join(args, " ")
		switch {
		case args[0] == "inspect":
			info := dockerInspection{}
			info.State.Running = true
			info.Config.Image = image
			data, _ := json.Marshal([]dockerInspection{info})
			return string(data), nil
		case strings.Contains(call, "pg_isready"):
			return "accepting connections", nil
		case strings.Contains(call, "max(version_id)"):
			return fmt.Sprint(schema), nil
		case strings.Contains(call, "version --help"):
			return "Flags:\n      --format string   output format: text or json\n", nil
		case strings.Contains(call, "version --json"), strings.Contains(call, "version --format json"):
			data, _ := json.Marshal(buildinfo.Identity{Revision: revision})
			return string(data), nil
		}
		return "", nil
	}
	if err := e.ExposeCandidate(t.Context(), e.id); err != nil {
		t.Fatalf("qualified legacy public predecessor could not activate candidate: %v", err)
	}
	marker, present, err := installationstate.ReadMarker(e.root)
	if err != nil || !present || marker.BootstrapPhase != installationstate.PhasePublic || marker.Image != e.id.Candidate || marker.TargetID != config.TargetID {
		t.Fatalf("candidate lost the retained installation identity: %+v, %v", marker, err)
	}
	saved, err := securefs.ReadPrivateFile(filepath.Join(e.operation, "original-config", installMarkerName))
	if err != nil || string(saved) != string(raw) {
		t.Fatal("activation rewrote the recovery marker")
	}
	if err := e.VerifyPredecessor(t.Context(), e.id); err != nil {
		t.Fatal(err)
	}
	restored, err := securefs.ReadPrivateFile(filepath.Join(e.root, installMarkerName))
	if err != nil || string(restored) != string(raw) {
		t.Fatal("recovery did not restore the exact legacy marker")
	}
}

func TestLegacyPublicMarkerCompatibilityIsExactAndNeverChangesTheSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any, *NativeRequest)
		mode   os.FileMode
	}{
		{name: "matching", mode: 0o600},
		{name: "different image", change: func(_ map[string]any, r *NativeRequest) { r.PredecessorImage = r.CandidateImage }, mode: 0o600},
		{name: "different source", change: func(_ map[string]any, r *NativeRequest) { r.PredecessorRevision = strings.Repeat("a", 40) }, mode: 0o600},
		{name: "marker image", change: func(m map[string]any, r *NativeRequest) { m["image"] = r.CandidateImage }, mode: 0o600},
		{name: "noncanonical image", change: func(m map[string]any, _ *NativeRequest) { m["image"] = legacyPublicImage + " " }, mode: 0o600},
		{name: "missing target", change: func(m map[string]any, _ *NativeRequest) { delete(m, "targetId") }, mode: 0o600},
		{name: "noncanonical target", change: func(m map[string]any, _ *NativeRequest) { m["targetId"] = " target " }, mode: 0o600},
		{name: "private phase", change: func(m map[string]any, _ *NativeRequest) { m["bootstrapPhase"] = installationstate.PhasePrivate }, mode: 0o600},
		{name: "null phase", change: func(m map[string]any, _ *NativeRequest) { m["bootstrapPhase"] = nil }, mode: 0o600},
		{name: "empty phase", change: func(m map[string]any, _ *NativeRequest) { m["bootstrapPhase"] = "" }, mode: 0o600},
		{name: "partial generation", change: func(m map[string]any, _ *NativeRequest) { m["generation"] = "wrong" }, mode: 0o600},
		{name: "unknown field", change: func(m map[string]any, _ *NativeRequest) { m["extra"] = true }, mode: 0o600},
		{name: "public file", mode: 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			r := NativeRequest{PredecessorImage: legacyPublicImage, PredecessorRevision: legacyPublicRevision}
			marker := map[string]any{"schemaVersion": 1, "domain": "demo.leapview.dev", "adminEmail": "admin@example.com",
				"environment": "prod", "image": legacyPublicImage, "https": true, "targetId": "retained-target"}
			if test.change != nil {
				test.change(marker, &r)
			}
			raw, err := json.Marshal(marker)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, installMarkerName)
			if err := os.WriteFile(path, raw, test.mode); err != nil {
				t.Fatal(err)
			}
			installed, err := readNativeUpgradeInstallation(root, r)
			if (err == nil) != (test.name == "matching") {
				t.Fatalf("unexpected historical admission: %+v, %v", installed, err)
			}
			if test.name == "matching" {
				if installed.Marker != nil || installed.Config.TargetID != marker["targetId"] {
					t.Fatal("legacy authority was not retained")
				}
				if _, err := readNativeInstallation(root); err == nil {
					t.Fatal("generic marker validation accepted the compatibility exception")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("admission changed the predecessor marker")
			}
		})
	}
}
