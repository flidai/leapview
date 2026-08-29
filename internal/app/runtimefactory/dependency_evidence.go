package runtimefactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

const (
	resultIdentityRuntimeDomain    = "flid.resultidentity.runtime.v1"
	resultIdentityCapabilityDomain = "flid.resultidentity.capability.v1"
)

// ActivationEvidence is verified, non-request-scoped runtime evidence retained
// with an active release or validated candidate compatibility contract.
type ActivationEvidence struct {
	RuntimeVersion     string
	BindingFingerprint string
	BindingKinds       map[string]string
	Capabilities       []runtimehost.RuntimeCapabilityEvidence
}

// ActivationEvidenceSource resolves immutable release evidence for an active
// serving identity. Candidate preparation supplies equivalent evidence through
// its already-validated runtime context.
type ActivationEvidenceSource interface {
	ResultIdentityEvidence(context.Context, projectgraph.ServingIdentity) (ActivationEvidence, error)
}

func dependencyEvidenceForRuntime(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	compiled projectbundle.CompiledProjectArtifact,
	artifact projectartifact.Project,
	managed runtimehost.ManagedDataResolution,
	candidate *runtimehost.CandidateRuntimeContext,
	source ActivationEvidenceSource,
) (map[string]resultidentity.Evidence, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	activation := ActivationEvidence{}
	if candidate != nil {
		activation = ActivationEvidence{
			RuntimeVersion: candidate.RuntimeVersion, BindingFingerprint: candidate.BindingFingerprint,
			BindingKinds: cloneStringMap(candidate.BindingKinds), Capabilities: cloneRuntimeCapabilities(candidate.Capabilities),
		}
	} else {
		if source == nil {
			return nil, fmt.Errorf("active result identity evidence source is unavailable")
		}
		var err error
		activation, err = source.ResultIdentityEvidence(ctx, identity)
		if err != nil {
			return nil, err
		}
	}
	return buildDependencyEvidence(compiled, artifact, managed.Revisions, activation)
}

func buildDependencyEvidence(
	compiled projectbundle.CompiledProjectArtifact,
	artifact projectartifact.Project,
	revisions map[string]string,
	activation ActivationEvidence,
) (map[string]resultidentity.Evidence, error) {
	runtimeDigest, err := versionedTextDigest(resultIdentityRuntimeDomain, activation.RuntimeVersion)
	if err != nil {
		return nil, err
	}
	capabilityDigest, err := capabilityDigest(activation.BindingKinds, activation.Capabilities)
	if err != nil {
		return nil, err
	}
	sourceDataEvidence := artifact.SourceDataIdentityEvidence(revisions, activation.BindingKinds)
	result := make(map[string]resultidentity.Evidence, len(compiled.Manifest.SemanticModels))
	for id, model := range compiled.Manifest.SemanticModels {
		semanticID, err := projectgraph.NewResourceID(id)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q: %w", id, err)
		}
		semanticDigest, err := semanticquery.SemanticModelDigest(model)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q dependency digest: %w", id, err)
		}
		relations, err := artifact.SemanticModelRelationEvidence(semanticID, sourceDataEvidence, activation.BindingKinds)
		if err != nil {
			return nil, err
		}
		if len(relations) == 0 {
			continue
		}
		datasetRelations := make([]resultidentity.DatasetRelation, len(relations))
		for index, relation := range relations {
			datasetRelations[index] = resultidentity.DatasetRelation{
				Dataset: relation.Dataset,
				Relation: resultidentity.RelationRevision{
					RelationID: relation.RelationID, RevisionDigest: relation.ExecutionDigest,
				},
			}
		}
		evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
			SemanticModelID: semanticID, SemanticModelDigest: semanticDigest,
			DatasetRelations: datasetRelations, BindingFingerprint: activation.BindingFingerprint,
			RuntimeDigest: runtimeDigest, CapabilityDigest: capabilityDigest,
		})
		if err != nil {
			return nil, fmt.Errorf("semantic model %q dependency evidence: %w", id, err)
		}
		result[id] = evidence
	}
	return result, nil
}

func versionedTextDigest(domain, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s value is required", domain)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func capabilityDigest(kinds map[string]string, capabilities []runtimehost.RuntimeCapabilityEvidence) (string, error) {
	type bindingKind struct {
		Connection string `json:"connection"`
		Kind       string `json:"kind"`
	}
	type canonicalCapability struct {
		Name             string `json:"name"`
		Identity         string `json:"identity"`
		Digest           string `json:"digest"`
		DuckDBVersion    string `json:"duckdbVersion"`
		ExtensionVersion string `json:"extensionVersion"`
		GOOS             string `json:"goos"`
		GOARCH           string `json:"goarch"`
		Platform         string `json:"platform"`
		SupportProfile   string `json:"supportProfile"`
	}
	connections := make([]string, 0, len(kinds))
	for connection := range kinds {
		connections = append(connections, connection)
	}
	sort.Strings(connections)
	bindings := make([]bindingKind, len(connections))
	for index, connection := range connections {
		kind := strings.TrimSpace(kinds[connection])
		if connection == "" || connection != strings.TrimSpace(connection) || kind == "" {
			return "", fmt.Errorf("runtime capability binding kinds are incomplete")
		}
		bindings[index] = bindingKind{Connection: connection, Kind: kind}
	}
	capabilities = cloneRuntimeCapabilities(capabilities)
	if len(capabilities) == 0 {
		return "", fmt.Errorf("runtime capability evidence is required")
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	duckDBVersion := ""
	canonicalCapabilities := make([]canonicalCapability, len(capabilities))
	for index, capability := range capabilities {
		for _, field := range []string{
			capability.Name, capability.Identity, capability.Digest, capability.DuckDBVersion,
			capability.ExtensionVersion, capability.GOOS, capability.GOARCH,
			capability.Platform, capability.SupportProfile,
		} {
			if field == "" || field != strings.TrimSpace(field) {
				return "", fmt.Errorf("runtime capability evidence is incomplete")
			}
		}
		if err := platformdigest.ValidateSHA256Identity(capability.Identity); err != nil {
			return "", fmt.Errorf("runtime capability %q identity: %w", capability.Name, err)
		}
		if err := platformdigest.ValidateSHA256Identity(capability.Digest); err != nil {
			return "", fmt.Errorf("runtime capability %q digest: %w", capability.Name, err)
		}
		if index > 0 && capabilities[index-1].Name == capability.Name {
			return "", fmt.Errorf("duplicate runtime capability %q", capability.Name)
		}
		if duckDBVersion == "" {
			duckDBVersion = capability.DuckDBVersion
		} else if capability.DuckDBVersion != duckDBVersion {
			return "", fmt.Errorf("runtime capability DuckDB versions are inconsistent")
		}
		canonicalCapabilities[index] = canonicalCapability{
			Name: capability.Name, Identity: capability.Identity, Digest: capability.Digest,
			DuckDBVersion: capability.DuckDBVersion, ExtensionVersion: capability.ExtensionVersion,
			GOOS: capability.GOOS, GOARCH: capability.GOARCH, Platform: capability.Platform,
			SupportProfile: capability.SupportProfile,
		}
	}
	encoded, err := json.Marshal(struct {
		Version      int                   `json:"version"`
		Bindings     []bindingKind         `json:"bindings"`
		Capabilities []canonicalCapability `json:"capabilities"`
	}{Version: 1, Bindings: bindings, Capabilities: canonicalCapabilities})
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(resultIdentityCapabilityDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneRuntimeCapabilities(values []runtimehost.RuntimeCapabilityEvidence) []runtimehost.RuntimeCapabilityEvidence {
	return append([]runtimehost.RuntimeCapabilityEvidence(nil), values...)
}
