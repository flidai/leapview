package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
)

type cacheLifecycleLedger struct {
	evidence identitymodule.LifecycleEvidence
	err      error
}

func (l cacheLifecycleLedger) ReadLifecycleEvidence(context.Context, string, projectgraph.ResourceID, projectgraph.Kind, string) (identitymodule.LifecycleEvidence, error) {
	return l.evidence, l.err
}

type cacheControlReader struct {
	states []access.AuthorizationControlRevision
	calls  int
}

func (r *cacheControlReader) ReadAuthorizationControlRevision(context.Context, string) (access.AuthorizationControlRevision, error) {
	index := r.calls
	r.calls++
	if index >= len(r.states) {
		index = len(r.states) - 1
	}
	return r.states[index], nil
}

func TestSemanticCacheLifecycleAdapter(t *testing.T) {
	binding := resultidentity.SemanticLifecycle{InstanceID: "instance-a", ProjectID: "project-a", AuthoredID: "model-a", ResourceKind: projectgraph.KindSemanticModel,
		Sequence: 2, ActiveBundleID: "bundle-a", PublicationVersion: "1.0.0", ProjectionProfile: contractprojection.Profile,
		PublicationDigest: "sha256:" + strings.Repeat("a", 64), AuthorizationRevision: 3}
	fixture := func() identitymodule.LifecycleEvidence {
		return identitymodule.LifecycleEvidence{Sequence: binding.Sequence,
			Identity: identitymodule.Identity{InstanceID: binding.InstanceID, AuthoredID: binding.AuthoredID, Kind: binding.ResourceKind, Lifecycle: identitymodule.LifecycleActive, ActiveBundleID: binding.ActiveBundleID},
			Publication: identitymodule.ContractPublication{InstanceID: binding.InstanceID, AuthoredID: binding.AuthoredID, ResourceKind: binding.ResourceKind,
				Version: binding.PublicationVersion, ProjectionProfile: binding.ProjectionProfile, Digest: binding.PublicationDigest}}
	}
	state := access.AuthorizationControlRevision{InstanceID: binding.InstanceID, ProjectID: "project-a", Revision: 3}
	for _, name := range []string{"profile", "version", "authorization revision"} {
		t.Run("invalid binding "+name, func(t *testing.T) {
			invalid := binding
			switch name {
			case "profile":
				invalid.ProjectionProfile = "unsupported/v1"
			case "version":
				invalid.PublicationVersion = "not-semver"
			case "authorization revision":
				invalid.AuthorizationRevision++
			}
			if _, err := SemanticCacheLifecycleReader(cacheLifecycleLedger{evidence: fixture()}, &cacheControlReader{states: []access.AuthorizationControlRevision{state}}, invalid, state); err == nil {
				t.Fatal("invalid activation binding accepted")
			}
		})
	}
	for _, name := range []string{"active", "tombstone", "wrong instance", "wrong kind", "wrong publication", "missing ledger", "control race", "wrong project", "restore", "rollback", "role change"} {
		t.Run(name, func(t *testing.T) {
			ledger := cacheLifecycleLedger{evidence: fixture()}
			control := &cacheControlReader{states: []access.AuthorizationControlRevision{state, state}}
			wantError := false
			switch name {
			case "tombstone":
				ledger.evidence.Identity.Lifecycle = identitymodule.LifecycleTombstoned
				wantError = true
			case "wrong instance":
				ledger.evidence.Identity.InstanceID = "other"
				wantError = true
			case "wrong kind":
				ledger.evidence.Identity.Kind = projectgraph.KindModel
				wantError = true
			case "wrong publication":
				ledger.evidence.Publication.AuthoredID = "other"
				wantError = true
			case "missing ledger":
				ledger.err = errors.New("missing")
				wantError = true
			case "control race":
				control.states[1].Revision++
				wantError = true
			case "wrong project":
				control.states[0].ProjectID = "other"
				wantError = true
			case "restore":
				ledger.evidence.Sequence++
				wantError = true
			case "rollback":
				ledger.evidence.Sequence++
				ledger.evidence.Identity.ActiveBundleID = "old-bundle"
				wantError = true
			case "role change":
				control.states[0].Revision++
				control.states[1].Revision++
				wantError = true
			}
			reader, err := SemanticCacheLifecycleReader(ledger, control, binding, state)
			if err != nil {
				t.Fatal(err)
			}
			current, err := reader(t.Context())
			if (err != nil) != wantError {
				t.Fatalf("error=%v wantError=%v", err, wantError)
			}
			if !wantError {
				if name == "active" && current != binding {
					t.Fatal("unchanged binding changed")
				}
				if name != "active" && current == binding {
					t.Fatal("transition resurrected stale binding")
				}
			}
		})
	}
}
