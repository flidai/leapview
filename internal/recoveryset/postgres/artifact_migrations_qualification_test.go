//go:build fai518qualification

package postgres_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

const artifactMigrationDirectory = "internal/platform/postgres/migrations"

// Only the admitted artifact's attested Git tree supplies SQL. In particular,
// never filter the checkout's MigrationFS: that would conceal extra migrations.
type artifactMigrationSet struct {
	predecessorAdmissionDigest string
	targetIdentityDigest       string
	identity                   transitionpreflight.ArtifactIdentity
	files                      fstest.MapFS
	graphDigest                string
	revision                   int64
}

func loadArtifactMigrations(ctx context.Context, identity transitionpreflight.ArtifactIdentity, expected int64) (artifactMigrationSet, error) {
	if _, err := identity.Digest(); err != nil {
		return artifactMigrationSet{}, err
	}
	revision := identity.Release.SourceRevision
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
		return artifactMigrationSet{}, errors.New("migration source requires exact admitted Git revision")
	}
	root, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return artifactMigrationSet{}, err
	}
	readGit := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = strings.TrimSpace(string(root))
		return command.Output()
	}
	names, err := readGit("ls-tree", "-r", "--name-only", revision, "--", artifactMigrationDirectory)
	if err != nil {
		return artifactMigrationSet{}, fmt.Errorf("read admitted migration tree: %w", err)
	}
	files := fstest.MapFS{}
	for _, name := range strings.Split(strings.TrimSpace(string(names)), "\n") {
		if path.Dir(name) != artifactMigrationDirectory || !strings.HasSuffix(name, ".sql") {
			continue
		}
		contents, err := readGit("show", revision+":"+name)
		if err != nil {
			return artifactMigrationSet{}, err
		}
		files[path.Base(name)] = &fstest.MapFile{Data: contents}
	}
	graph, err := artifactMigrationGraph(files, expected)
	if err != nil {
		return artifactMigrationSet{}, err
	}
	declaration, err := readGit("show", revision+":"+artifactMigrationDirectory+"/goose.go")
	if err != nil {
		return artifactMigrationSet{}, err
	}
	if !regexp.MustCompile(fmt.Sprintf(`CurrentRevision\s+int64\s*=\s*%d\b`, expected)).Match(declaration) {
		return artifactMigrationSet{}, errors.New("admitted source schema declaration disagrees with migration set")
	}
	return artifactMigrationSet{identity: identity, files: files, graphDigest: graph, revision: expected}, nil
}

func artifactMigrationGraph(source fs.FS, expected int64) (string, error) {
	if expected != 19 && expected != 20 {
		return "", errors.New("qualification requires revision 019 or 020")
	}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return "", err
	}
	if int64(len(entries)) != expected {
		return "", fmt.Errorf("candidate migration set has %d entries, want exactly %d", len(entries), expected)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("leapview/fai518-qualification-migration-set/v1\n"))
	for index, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), fmt.Sprintf("%03d_", index+1)) || !strings.HasSuffix(entry.Name(), ".sql") {
			return "", fmt.Errorf("unexpected candidate migration %s", entry.Name())
		}
		contents, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s\x00%x\n", entry.Name(), sha256.Sum256(contents))
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

// This is qualification evidence, not a new migration authority contract.
type artifactMigrationEvidence struct {
	PredecessorAdmissionDigest string `json:"predecessorArtifactAdmissionDigest"`
	CandidateAdmissionDigest   string `json:"candidateArtifactAdmissionDigest"`
	TargetIdentityDigest       string `json:"targetIdentityDigest"`
	CandidateImage             string `json:"candidateImage"`
	CandidateSourceRevision    string `json:"candidateSourceRevision"`
	MigrationGraphDigest       string `json:"migrationGraphDigest"`
	CapabilityDigest           string `json:"capabilityDigest"`
	PredecessorRevision        int64  `json:"predecessorRevision"`
	CandidateRevision          int64  `json:"candidateRevision"`
	ObservedRevision           int64  `json:"observedRevision"`
}

func (set artifactMigrationSet) bind(evidence transitionpreflight.Evidence, capability migrationcapability.Capability) (artifactMigrationEvidence, error) {
	graph, err := artifactMigrationGraph(set.files, 20)
	if err != nil {
		return artifactMigrationEvidence{}, err
	}
	if set.revision != 20 || graph != set.graphDigest || set.identity != evidence.Candidate || evidence.Predecessor.ArtifactAdmissionDigest == "" || evidence.Predecessor.ArtifactAdmissionDigest != set.predecessorAdmissionDigest || evidence.TargetIdentityDigest == "" || evidence.TargetIdentityDigest != set.targetIdentityDigest || evidence.Control.TargetIdentityDigest != evidence.TargetIdentityDigest || evidence.Control.PredecessorSchemaVersion != "goose/v19" || evidence.Control.CandidateSchemaVersion != "goose/v20" {
		return artifactMigrationEvidence{}, errors.New("candidate migration artifact/preflight binding mismatch")
	}
	if capability.ArtifactAdmissionDigest != evidence.Candidate.ArtifactAdmissionDigest || capability.TargetIdentityDigest != evidence.TargetIdentityDigest || capability.Subsystem != migrationcapability.SubsystemGoose || capability.Goose == nil || capability.Goose.SchemaVersion != "goose/v20" || capability.Goose.MigrationGraphDigest != graph {
		return artifactMigrationEvidence{}, errors.New("candidate migration capability binding mismatch")
	}
	digest, err := capability.Digest()
	if err != nil {
		return artifactMigrationEvidence{}, err
	}
	return artifactMigrationEvidence{
		PredecessorAdmissionDigest: evidence.Predecessor.ArtifactAdmissionDigest, CandidateAdmissionDigest: evidence.Candidate.ArtifactAdmissionDigest,
		TargetIdentityDigest: evidence.TargetIdentityDigest, CandidateImage: set.identity.Release.Image, CandidateSourceRevision: set.identity.Release.SourceRevision,
		MigrationGraphDigest: graph, CapabilityDigest: digest, PredecessorRevision: 19, CandidateRevision: 20,
	}, nil
}

func (set artifactMigrationSet) apply(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, evidence transitionpreflight.Evidence, capability migrationcapability.Capability) (artifactMigrationEvidence, error) {
	bound, err := set.bind(evidence, capability)
	if err != nil {
		return bound, err
	}
	err = migrations.WithMigrationFence(ctx, pool, func() error {
		provider, err := goose.NewProvider(goose.DialectPostgres, db, set.files)
		if err != nil {
			return err
		}
		current, available, err := provider.GetVersions(ctx)
		if err != nil {
			return err
		}
		if current != 19 || available != 20 {
			return fmt.Errorf("candidate migration boundary is %d→%d, want 19→20", current, available)
		}
		if _, err := provider.Up(ctx); err != nil {
			return err
		}
		bound.ObservedRevision, _, err = provider.GetVersions(ctx)
		if err != nil {
			return err
		}
		return bound.validateObserved(bound.ObservedRevision)
	})
	return bound, err
}

func (bound artifactMigrationEvidence) validateObserved(observed int64) error {
	if bound.PredecessorRevision != 19 || bound.CandidateRevision != 20 || observed != 20 {
		return fmt.Errorf("candidate 019→020 qualification observed revision %d", observed)
	}
	return nil
}

func (bound artifactMigrationEvidence) payload() ([]byte, error) { return json.Marshal(bound) }
