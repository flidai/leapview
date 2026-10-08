//go:build linux

package managedmaintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/artifactadmission"
)

func TestKamalPreflightBindsProducerAdmissionAndLiveEnvironment(t *testing.T) {
	for _, variant := range []string{"valid", "receipt-replaced", "configuration-drift", "credential-drift", "candidate-env-drift", "missing-image", "running-candidate", "closed-predecessor", "capacity-bytes", "capacity-inodes", "capacity-policy-missing", "legacy-recovery", "capacity-policy-drift", "capacity-docker-drift",
		"enroll-empty", "enroll-closed", "enroll-admitted", "enroll-prepared", "enroll-other-operation", "enroll-published-proxy", "enroll-open-gate", "enroll-missing-gate", "enroll-malformed-gate", "enroll-wrong-content", "enroll-recover-closed", "enroll-recover-own-operation", "enroll-recover-admitted", "enroll-recover-other-operation", "enroll-recover-unbound-admitted", "enroll-recover-capacity-missing"} {
		t.Run(variant, func(t *testing.T) {
			k, app, proxy := adapterFixture(t)
			enrollment := strings.HasPrefix(variant, "enroll-")
			if enrollment {
				k.Request.Operation = "enroll"
				k.recovering = strings.HasPrefix(variant, "enroll-recover-")
				app.Image = "content"
			}
			dockerRoot := k.Profile.Capacity.DockerRootDir
			if variant == "capacity-policy-missing" || variant == "legacy-recovery" || variant == "enroll-recover-capacity-missing" {
				k.Profile.Capacity = nil
				k.recovering = variant != "capacity-policy-missing"
			}
			if variant == "capacity-bytes" || variant == "capacity-inodes" {
				k.capacityProbe = func(string) (filesystemCapacity, error) {
					m := filesystemCapacity{Device: "1", FreeBytes: 100, FreeInodes: 100}
					if variant == "capacity-bytes" {
						m.FreeBytes = 2
					} else {
						m.FreeInodes = 2
					}
					return m, nil
				}
			}
			for _, name := range profileFiles {
				if name == "environment.json" {
					continue
				}
				path := filepath.Join(k.Profile.Root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			configuration, err := ProfileDigest(k.Profile)
			if err != nil {
				t.Fatal(err)
			}
			k.Request.Predecessor.ConfigurationDigest = configuration
			k.Request.Candidate.ConfigurationDigest = configuration
			for _, release := range []*Release{&k.Request.Predecessor, &k.Request.Candidate} {
				_, digest, _ := strings.Cut(release.Image, "@")
				evidence := "sha256:" + strings.Repeat("f", 64)
				record := artifactadmission.Admission{Version: artifactadmission.AdmissionVersion, Release: compatibility.ReleaseIdentity{ReleaseID: release.Revision, Version: "v1.2.3", SourceRevision: release.Revision, Image: release.Image, Distribution: "distroless", Platform: "linux/amd64"}, ArchitectureMarker: "postgres-control/v1", Repository: "ghcr.io/flidai/leapview", OCIDigest: digest, Decision: artifactadmission.DecisionAdmitted, Provenance: artifactadmission.ProvenanceResult{Reference: evidence, Repository: artifactadmission.SourceRepository, Workflow: "flidai/leapview/.github/workflows/release.yml", SourceRevision: release.Revision, Verified: true}, SBOM: artifactadmission.SBOMResult{Reference: evidence, PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerBuildx, Verified: true}, SecurityPolicy: artifactadmission.SecurityPolicyResult{Version: artifactadmission.SecurityPolicyVersion, Reference: evidence, Scanner: artifactadmission.SecurityScannerTrivy, Passed: true}, AdmittedAt: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)}
				raw, err := record.CanonicalJSON()
				if err != nil {
					t.Fatal(err)
				}
				release.ArtifactAdmissionDigest, err = record.Digest()
				if err != nil {
					t.Fatal(err)
				}
				if variant == "receipt-replaced" && release == &k.Request.Candidate {
					raw = []byte(`{"decision":"admitted"}`)
				}
				if err = os.WriteFile(filepath.Join(k.Profile.AdmissionRoot, release.ArtifactAdmissionDigest[7:]+".json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if enrollment {
				k.Request.Candidate = k.Request.Predecessor
				if variant != "enroll-missing-gate" {
					if err = writeGate(k.Profile, variant == "enroll-open-gate" || variant == "enroll-recover-admitted"); err != nil {
						t.Fatal(err)
					}
				}
				if variant == "enroll-malformed-gate" {
					if err = os.WriteFile(filepath.Join(k.Profile.StateRoot, "ingress.json"), []byte(`{}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if variant == "enroll-published-proxy" || variant == "enroll-recover-admitted" {
					proxy.HostConfig.PortBindings = map[string][]portBinding{"443/tcp": {{HostPort: "443"}}}
				}
				if variant == "enroll-wrong-content" {
					app.Image = "different-content"
				}
			}
			environment := map[string]string{}
			for _, v := range app.Config.Env {
				key, value, _ := strings.Cut(v, "=")
				environment[key] = value
			}
			switch variant {
			case "capacity-policy-drift":
				k.Profile.Capacity.Home.FreeBytes++
			case "capacity-docker-drift":
				dockerRoot = filepath.Join(t.TempDir(), "another-daemon-root")
			case "configuration-drift":
				if err = os.WriteFile(filepath.Join(k.Profile.Root, "deploy.yml"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "credential-drift":
				app.Config.Env = append(app.Config.Env, "LEAPVIEW_UNKNOWN_CREDENTIAL=changed")
			case "running-candidate":
				app.Config.Image = k.Request.Candidate.Image
				app.Name = "/leapview-web-" + k.Request.Candidate.Revision
			}
			privateControl(t, k, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/status" {
					t.Error("preflight mutated private control")
				}
				state := "admitted"
				operation := ""
				if enrollment {
					state = "closed"
					switch variant {
					case "enroll-admitted", "enroll-recover-admitted", "enroll-recover-unbound-admitted":
						state = "admitted"
					case "enroll-prepared", "enroll-recover-own-operation":
						state = "prepared"
					}
					if variant == "enroll-other-operation" || variant == "enroll-recover-other-operation" {
						operation = "sha256:" + strings.Repeat("f", 64)
					}
					if variant == "enroll-recover-own-operation" || variant == "enroll-recover-admitted" {
						operation, _ = k.Request.Digest()
					}
				}
				if variant == "closed-predecessor" {
					state = "closed"
				}
				_ = json.NewEncoder(w).Encode(controlStatus{Revision: k.Request.Predecessor.Revision, State: state, Operation: operation})
			})
			k.run = func(_ context.Context, bin string, args, env []string, _ string) ([]byte, error) {
				encode := func(value any) ([]byte, error) { return json.Marshal(value) }
				if bin == "bundle" {
					if strings.Join(args, " ") != "exec ruby maintenance_config.rb" {
						t.Fatalf("preflight mutated Kamal: %v", args)
					}
					values := map[string]string{}
					for _, v := range env {
						key, value, _ := strings.Cut(v, "=")
						values[key] = value
					}
					resolved := map[string]string{}
					for key, value := range environment {
						resolved[key] = value
					}
					if variant == "candidate-env-drift" && values["LEAPVIEW_MANAGED_IMAGE"] == k.Request.Candidate.Image {
						resolved["LEAPVIEW_CSRF_KEY"] = "changed"
					}
					return encode(renderedProfile{Service: k.Profile.Service, Hosts: []string{"127.0.0.1"}, Roles: []string{"web"}, Hostname: k.Profile.Hostname, Environment: resolved, Image: values["LEAPVIEW_MANAGED_IMAGE"], ProxyImage: k.Profile.ProxyImage})
				}
				if bin != "docker" {
					t.Fatalf("unexpected preflight command %s", bin)
				}
				action := strings.Join(args[2:], " ")
				switch {
				case action == "info --format {{json .DockerRootDir}}":
					return encode(dockerRoot)
				case strings.HasPrefix(action, "image inspect "):
					reference := args[len(args)-1]
					revision := k.Request.Predecessor.Revision
					if reference == k.Request.Candidate.Image {
						revision = k.Request.Candidate.Revision
					}
					info := imageInfo{ID: "content", RepoDigests: []string{reference}}
					info.Config.Labels = map[string]string{"org.opencontainers.image.revision": revision}
					if variant == "missing-image" && reference == k.Request.Candidate.Image {
						info.RepoDigests = nil
					}
					return encode([]imageInfo{info})
				case action == "container ls --all --quiet --no-trunc":
					if variant == "enroll-empty" {
						return nil, nil
					}
					return []byte("app-id\nproxy-id\n"), nil
				case action == "container inspect app-id proxy-id":
					return encode([]containerInfo{app, proxy})
				default:
					t.Fatalf("preflight mutating/unexpected Docker command %s", action)
					return nil, nil
				}
			}
			err = k.Preflight(t.Context())
			wantOK := variant == "valid" || variant == "legacy-recovery" || variant == "enroll-empty" || variant == "enroll-closed" || variant == "enroll-recover-closed" || variant == "enroll-recover-own-operation" || variant == "enroll-recover-admitted"
			if (err == nil) != wantOK {
				t.Fatalf("Preflight = %v", err)
			}
			if strings.HasPrefix(variant, "capacity-") || (enrollment && !wantOK) {
				// Exercise the production adapter through the coordinator: failed
				// resource admission must precede private RPCs or any closure.
				journal := &memoryJournal{}
				if err := (&Coordinator{Request: k.Request, Journal: journal, Effects: k}).Run(t.Context()); err == nil || journal.exists {
					t.Fatalf("capacity rejection mutated operation: %v, %+v", err, journal)
				}
			}
		})
	}
}

func TestOperatorInputRejectsWritableAndSymlinkAncestry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "input.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorFile(path); err == nil {
		t.Fatal("accepted writable input ancestry")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "operator")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorFile(filepath.Join(link, "input.json")); err == nil {
		t.Fatal("accepted symlink input ancestry")
	}
}
