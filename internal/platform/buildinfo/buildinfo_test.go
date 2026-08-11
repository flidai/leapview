package buildinfo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDevelopmentIdentityNeverClaimsReleaseVersion(t *testing.T) {
	identity := resolve(injectedMetadata{
		version:   "0.2.0-rc.1",
		revision:  strings.Repeat("a", 40),
		buildTime: "2026-07-27T12:00:00Z",
		dirty:     "false",
		release:   "false",
	}, vcsMetadata{})

	if identity.Version != DevelopmentVersion || !identity.Development {
		t.Fatalf("development identity = %#v", identity)
	}
	if identity.Revision != strings.Repeat("a", 40) {
		t.Fatalf("development revision = %q", identity.Revision)
	}
}

func TestDevelopmentIdentityPreservesQualifiedBuildVersion(t *testing.T) {
	metadata := injectedMetadata{
		version:   "0.2.0-rc.1+main.248521fd0cd8",
		revision:  strings.Repeat("a", 40),
		buildTime: "2026-08-07T07:33:28Z",
		dirty:     "false",
		release:   "false",
	}
	identity := resolve(metadata, vcsMetadata{})

	if identity.Version != metadata.version || identity.Revision != metadata.revision || identity.BuildTime != metadata.buildTime {
		t.Fatalf("qualified development identity = %#v", identity)
	}
	if !identity.Development || identity.Dirty {
		t.Fatalf("qualified development state = %#v", identity)
	}
}

func TestReleaseIdentityRequiresCompleteValidatedMetadata(t *testing.T) {
	valid := injectedMetadata{
		version:   "0.2.0-rc.1",
		revision:  strings.Repeat("a", 40),
		buildTime: "2026-07-27T12:00:00Z",
		dirty:     "false",
		release:   "true",
	}
	identity := resolve(valid, vcsMetadata{})
	if identity.Version != valid.version || identity.Revision != valid.revision || identity.BuildTime != valid.buildTime {
		t.Fatalf("release identity = %#v", identity)
	}
	if identity.Development || identity.Dirty {
		t.Fatalf("release state = %#v", identity)
	}

	for name, mutate := range map[string]func(*injectedMetadata){
		"non-semver version": func(value *injectedMetadata) { value.version = "candidate-123" },
		"short revision":     func(value *injectedMetadata) { value.revision = "abc123" },
		"invalid build time": func(value *injectedMetadata) { value.buildTime = "today" },
		"dirty release":      func(value *injectedMetadata) { value.dirty = "true" },
	} {
		t.Run(name, func(t *testing.T) {
			metadata := valid
			mutate(&metadata)
			got := resolve(metadata, vcsMetadata{})
			if !got.Development || got.Version != DevelopmentVersion {
				t.Fatalf("invalid release metadata claimed a release identity: %#v", got)
			}
		})
	}
}

func TestDevelopmentIdentityUsesGoVCSMetadata(t *testing.T) {
	identity := resolve(injectedMetadata{}, vcsMetadata{
		revision: strings.Repeat("b", 40),
		time:     "2026-07-27T11:00:00Z",
		modified: true,
	})
	if identity.Version != DevelopmentVersion || identity.Revision != strings.Repeat("b", 40) {
		t.Fatalf("development identity = %#v", identity)
	}
	if identity.BuildTime != "2026-07-27T11:00:00Z" || !identity.Dirty || !identity.Development {
		t.Fatalf("development metadata = %#v", identity)
	}
}

func TestWriteReportsStableHumanAndJSONIdentity(t *testing.T) {
	identity := Identity{
		Version: "0.2.0-rc.1", Revision: strings.Repeat("c", 40),
		BuildTime: "2026-07-27T10:00:00Z",
	}
	var human bytes.Buffer
	if err := Write(&human, "leapview", identity, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"leapview 0.2.0-rc.1",
		"revision: " + strings.Repeat("c", 40),
		"build time: 2026-07-27T10:00:00Z",
		"state: release, clean",
	} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human output missing %q:\n%s", want, human.String())
		}
	}

	var machine bytes.Buffer
	if err := Write(&machine, "leapview", identity, true); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Product string `json:"product"`
		Identity
	}
	if err := json.Unmarshal(machine.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, machine.String())
	}
	if decoded.Product != "leapview" || decoded.Identity != identity {
		t.Fatalf("JSON identity = %#v", decoded)
	}
}
