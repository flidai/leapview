package compiler

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
)

func validateProject(project Project) error {
	for connectionName, connection := range project.Connections {
		if _, err := connection.ValidateAuthored(connectionName); err != nil {
			return resourceError(project.ConnectionPaths[connectionName], project.ConnectionIDs[connectionName], "spec", "Connection %q %s", connectionName, err.Error())
		}
	}
	for sourceName, source := range project.Sources {
		if source.Path != "" && source.Format == "" {
			format, ok := connectors.InferFormat(source.Path)
			if !ok {
				return resourceError(project.SourcePaths[sourceName], project.SourceIDs[sourceName], "spec.format", "Source %q path %q requires format", sourceName, source.Path)
			}
			source.Format = format
			project.Sources[sourceName] = source
		}
		if err := source.Validate(localSourceName(sourceName), project.Connections); err != nil {
			return resourceError(project.SourcePaths[sourceName], project.SourceIDs[sourceName], "spec", "Source %q %s", sourceName, err.Error())
		}
	}
	return validateFlatProject(project)
}

func validatePublicationOrigin(authored string) (string, error) {
	if authored == "" || strings.TrimSpace(authored) != authored {
		return "", fmt.Errorf("must be a non-empty exact origin")
	}
	if strings.Contains(authored, "*") {
		return "", fmt.Errorf("must not contain wildcards")
	}
	parsed, err := url.Parse(authored)
	if err != nil || parsed.IsAbs() == false || parsed.Host == "" || parsed.Opaque != "" {
		return "", fmt.Errorf("must be an absolute HTTP(S) origin")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("must not contain credentials")
	}
	if parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("must contain an origin only, without path, query, or fragment")
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("must include a hostname")
	}
	if parsed.Scheme != "https" {
		loopback := strings.EqualFold(hostname, "localhost")
		if ip := net.ParseIP(hostname); ip != nil {
			loopback = ip.IsLoopback()
		}
		if parsed.Scheme != "http" || !loopback {
			return "", fmt.Errorf("must use https except for loopback development origins")
		}
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
