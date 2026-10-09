package metadata

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

func canonicalDataPath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("DuckLake DATA_PATH contains a control character")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("DuckLake DATA_PATH is required for a shared physical pool")
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
			return "", fmt.Errorf("DuckLake DATA_PATH URL is invalid")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		if strings.ContainsAny(parsed.Path, "\x00\r\n") {
			return "", fmt.Errorf("DuckLake DATA_PATH URL contains a control character")
		}
		if parsed.Scheme == "file" {
			if parsed.Host != "" || parsed.Path == "" {
				return "", fmt.Errorf("DuckLake DATA_PATH file URL is invalid")
			}
			return canonicalLocalPath(parsed.Path)
		}
		if parsed.Scheme != "s3" && parsed.Scheme != "gs" && parsed.Scheme != "az" && parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", fmt.Errorf("DuckLake DATA_PATH scheme %q is unsupported", parsed.Scheme)
		}
		parsed.Path = path.Clean(parsed.Path)
		parsed.RawPath = ""
		return parsed.String(), nil
	}
	return canonicalLocalPath(value)
}

// CanonicalDataPath exposes the same storage-path normalization used by
// physical-pool admission to operation packages that verify an attached
// DuckLake catalog. Keeping one implementation prevents false mismatches for
// URL host casing, trailing separators, and local relative paths.
func CanonicalDataPath(value string) (string, error) {
	return canonicalDataPath(value)
}

func canonicalLocalPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize local DuckLake DATA_PATH: %w", err)
	}
	return filepath.Clean(absolute), nil
}
