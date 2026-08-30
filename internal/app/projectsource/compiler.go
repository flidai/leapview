package projectsource

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
)

// Compiler is the native source compiler. It accepts only the staged logical
// source set supplied in CompileInput and never resolves an authoritative
// project path on the host filesystem.
type Compiler struct{}

var _ CompilerPort = Compiler{}

func (Compiler) Compile(ctx context.Context, input CompileInput) (CompileOutput, error) {
	if ctx == nil {
		return CompileOutput{}, fmt.Errorf("compile context is required")
	}
	if err := ctx.Err(); err != nil {
		return CompileOutput{}, err
	}
	if input.ProjectID == "" || len(input.ProjectID) > maxProjectIDBytes || !validText(input.ProjectID) ||
		input.StorageSecurityDomain == "" || len(input.StorageSecurityDomain) > maxProjectIDBytes || !validText(input.StorageSecurityDomain) ||
		!canonicalPath(input.ProjectFile) || input.SourceDigest == "" || !validDigest(input.SourceDigest) {
		return CompileOutput{}, fmt.Errorf("project identity, storage security domain, project file, and source digest are required")
	}
	files := make(map[string][]byte, len(input.Files))
	entries := make([]projectpostgres.SourceSnapshotEntryInput, 0, len(input.Files))
	for _, source := range input.Files {
		if !canonicalPath(source.Path) {
			return CompileOutput{}, fmt.Errorf("source path %q is not project-relative", source.Path)
		}
		if source.StorageSecurityDomain != input.StorageSecurityDomain {
			return CompileOutput{}, fmt.Errorf("source path %q has storage security domain %q, want %q", source.Path, source.StorageSecurityDomain, input.StorageSecurityDomain)
		}
		if source.Digest == "" || source.Digest != sha256Identity(source.Bytes) {
			return CompileOutput{}, fmt.Errorf("source path %q digest does not match bytes", source.Path)
		}
		if _, exists := files[source.Path]; exists {
			return CompileOutput{}, fmt.Errorf("duplicate source path %q", source.Path)
		}
		files[source.Path] = append([]byte(nil), source.Bytes...)
		entries = append(entries, projectpostgres.SourceSnapshotEntryInput{Path: source.Path, Digest: source.Digest, SizeBytes: int64(len(source.Bytes))})
	}
	if len(files) == 0 {
		return CompileOutput{}, fmt.Errorf("project source files are required")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	wantSourceDigest := projectpostgres.CanonicalSourceDigest(input.ProjectID, input.ProjectFile, entries)
	if input.SourceDigest != wantSourceDigest {
		return CompileOutput{}, fmt.Errorf("source digest %q does not match canonical source digest %q", input.SourceDigest, wantSourceDigest)
	}
	compiled, err := projectcompiler.CompileProjectFiles(files, input.ProjectFile)
	if err != nil {
		return CompileOutput{}, err
	}
	if err := ctx.Err(); err != nil {
		return CompileOutput{}, err
	}
	if string(compiled.ProjectID()) != input.ProjectID {
		return CompileOutput{}, fmt.Errorf("project id %q does not match compiled project %q", input.ProjectID, compiled.ProjectID())
	}
	manifest, err := json.Marshal(compiled.Manifest())
	if err != nil {
		return CompileOutput{}, fmt.Errorf("encode project manifest: %w", err)
	}
	return CompileOutput{
		ProjectDigest:   compiled.Digest(),
		CompilerVersion: projectartifact.CompilerVersion,
		SchemaVersion:   projectartifact.Version,
		ProjectArtifact: compiled.Canonical(),
		Manifest:        manifest,
	}, nil
}
