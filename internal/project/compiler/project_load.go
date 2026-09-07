package compiler

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

type sourceFileReader interface {
	ReadFile(string) ([]byte, error)
	document.FragmentReader
}

type osSourceReader struct{ document.OSFragmentReader }

func (osSourceReader) ReadFile(path string) ([]byte, error) {
	return securefs.ReadCanonicalFile(path)
}

func loadConnections(project *sourceAssembly, paths []string, reader sourceFileReader) error {
	for _, path := range paths {
		envelope, err := readEnvelope(reader, path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Connection" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Connection", path, envelope.Kind)
		}
		content, err := reader.ReadFile(path)
		if err != nil {
			return err
		}
		spec, err := decodeConnectionResource(path, content, envelope.Metadata)
		if err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if envelope.Metadata.ID == "" {
			return resourceError(path, "", "metadata.id", "%s metadata.id is required", path)
		}
		if _, err := projectgraph.NewResourceID(envelope.Metadata.ID); err != nil {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "%s metadata.id: %v", path, err)
		}
		if _, exists := project.Connections[name]; exists {
			return resourceError(path, "connection:"+name, "metadata.name", "duplicate Connection %q", name)
		}
		project.Connections[name] = spec
		project.ConnectionPaths[name] = path
		if owner, exists := project.ResourceIDOwners[envelope.Metadata.ID]; exists {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "duplicate resource id %q already used by %s", envelope.Metadata.ID, owner)
		}
		project.ResourceIDOwners[envelope.Metadata.ID] = "connection:" + name
		project.ConnectionIDs[name] = envelope.Metadata.ID
		project.ResourceIDs["connection:"+name] = envelope.Metadata.ID
		project.ResourcePaths[envelope.Metadata.ID] = path
		project.ResourceMetadata[envelope.Metadata.ID] = flatResourceMetadata(envelope.Metadata, name)
	}
	return nil
}

func loadSources(project *sourceAssembly, paths []string, reader sourceFileReader) error {
	for _, path := range paths {
		envelope, err := readEnvelope(reader, path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Source" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Source", path, envelope.Kind)
		}
		content, err := reader.ReadFile(path)
		if err != nil {
			return err
		}
		source, err := decodeSourceResource(path, content, envelope.Metadata)
		if err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if envelope.Metadata.ID == "" {
			return resourceError(path, "", "metadata.id", "%s metadata.id is required", path)
		}
		if _, err := projectgraph.NewResourceID(envelope.Metadata.ID); err != nil {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "%s metadata.id: %v", path, err)
		}
		if _, exists := project.Sources[name]; exists {
			return resourceError(path, "source:"+name, "metadata.name", "duplicate Source %q", name)
		}
		project.Sources[name] = source
		project.SourcePaths[name] = path
		if owner, exists := project.ResourceIDOwners[envelope.Metadata.ID]; exists {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "duplicate resource id %q already used by %s", envelope.Metadata.ID, owner)
		}
		project.ResourceIDOwners[envelope.Metadata.ID] = "source:" + name
		project.SourceIDs[name] = envelope.Metadata.ID
		project.ResourceIDs["source:"+name] = envelope.Metadata.ID
		project.ResourcePaths[envelope.Metadata.ID] = path
		project.ResourceMetadata[envelope.Metadata.ID] = flatResourceMetadata(envelope.Metadata, name)
		project.ResourceSources[envelope.Metadata.ID] = string(content)
	}
	return nil
}

func readEnvelope(reader sourceFileReader, path string) (resourceEnvelope, error) {
	content, err := reader.ReadFile(path)
	if err != nil {
		return resourceEnvelope{}, err
	}
	if kind, ok := schemaKindForEnvelope(content); ok {
		if err := configschema.ValidateBytes(kind, path, content); err != nil {
			return resourceEnvelope{}, annotateSchemaError(err, path, resourceIDForHeader(content, ""), "spec")
		}
	}
	var envelope resourceEnvelope
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&envelope); err != nil {
		return resourceEnvelope{}, fmt.Errorf("%s: %w", path, err)
	}
	if envelope.APIVersion != sourceAPIVersion {
		return resourceEnvelope{}, resourceError(path, envelopeResourceID(envelope, ""), "apiVersion", "%s apiVersion = %q, want %q", path, envelope.APIVersion, sourceAPIVersion)
	}
	if envelope.Kind == "" {
		return resourceEnvelope{}, resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind is required", path)
	}
	return envelope, nil
}

func resourceIDForHeader(content []byte, fallback string) string {
	var envelope resourceEnvelope
	if err := yaml.Unmarshal(content, &envelope); err != nil {
		return ""
	}
	return envelopeResourceID(envelope, fallback)
}

func envelopeResourceID(envelope resourceEnvelope, fallback string) string {
	name := strings.TrimSpace(envelope.Metadata.Name)
	if name == "" {
		return strings.TrimSpace(envelope.Metadata.ID)
	}
	if id := strings.TrimSpace(envelope.Metadata.ID); id != "" {
		return id
	}
	prefix := map[string]string{
		"Connection": "connection:", "Source": "source:",
		"Model": "model:", "SemanticModel": "semantic_model:", "Pipeline": "pipeline:",
		"Dashboard": "dashboard:",
	}[envelope.Kind]
	if prefix == "" {
		return strings.TrimSpace(fallback)
	}
	return prefix + name
}

func schemaKindForEnvelope(content []byte) (configschema.Kind, bool) {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil || header.APIVersion != sourceAPIVersion {
		return "", false
	}
	kinds := map[string]configschema.Kind{
		"Connection": configschema.KindConnection, "Source": configschema.KindSource,
		"Model": configschema.KindModel, "SemanticModel": configschema.KindSemanticModel, "Pipeline": configschema.KindPipeline,
		"Dashboard": configschema.KindDashboard,
	}
	kind, ok := kinds[header.Kind]
	return kind, ok
}

func projectConfigFile(path string) bool {
	content, err := securefs.ReadCanonicalFile(path)
	if err != nil {
		return false
	}
	var envelope struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &envelope); err != nil {
		return false
	}
	return envelope.APIVersion == sourceAPIVersion && envelope.Kind == "Project"
}
