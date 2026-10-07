package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDevelopmentCredentialTargetIsLoopbackOnly(t *testing.T) {
	for _, raw := range []string{"postgres://user:secret@127.0.0.1:5432/db", "postgresql://user:secret@localhost/db", "postgres://user:secret@[::1]:5432/db"} {
		if !isLoopbackDatabase(raw) {
			t.Fatalf("rejected loopback database %q", raw)
		}
	}
	for _, raw := range []string{"postgres://user:secret@db.example.com/db", "postgres://user:secret@10.0.0.1/db", "file:///tmp/db"} {
		if isLoopbackDatabase(raw) {
			t.Fatalf("accepted non-loopback database %q", raw)
		}
	}
}

func TestDevelopmentCredentialBundleIsPrivateAndPublisherIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	bundle := accesspostgres.DevelopmentCredentials{Email: "dev@localhost", Password: "secret", PublisherToken: "token", PublisherTokenExpiresAt: time.Now().UTC()}
	if err := writeCredentials(path, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := securefs.ReadPrivateFile(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode=%v, want 0600", info.Mode())
	}
	pairs, err := developmentPublisherPermissions(projectgraph.ResourceID("project_demo"))
	if err != nil || len(pairs) == 0 || !samePermissions(pairs, append([]access.PermissionPair(nil), pairs...)) {
		t.Fatalf("publisher permissions = %#v, err=%v", pairs, err)
	}
	want, err := access.InitialProjectPublisherPermissions(projectgraph.ResourceID("project_demo"))
	if err != nil || !samePermissions(pairs, want) {
		t.Fatalf("publisher permissions = %#v, want initial publisher permissions %#v, err=%v", pairs, want, err)
	}
	if samePermissions(pairs, pairs[:len(pairs)-1]) {
		t.Fatal("incomplete publisher permissions were accepted")
	}
}

func TestDevelopmentBootstrapUsesExactInitialClaimPermission(t *testing.T) {
	const instanceID = "instance_development"
	pairs, err := developmentBootstrapPermissions(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := access.InitialProjectClaimPermissions(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !samePermissions(pairs, want) || len(pairs) != 1 {
		t.Fatalf("development bootstrap permissions = %#v, want exact claim permission %#v", pairs, want)
	}
}
