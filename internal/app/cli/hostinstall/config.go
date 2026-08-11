package hostinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

func readAndValidateConfig(path string) (Config, composectl.InitOptions, error) {
	contents, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Config{}, composectl.InitOptions{}, fmt.Errorf("configuration must contain exactly one JSON object")
	}
	if config.SchemaVersion != 1 {
		return Config{}, composectl.InitOptions{}, fmt.Errorf("unsupported schemaVersion %d", config.SchemaVersion)
	}
	if config.HTTPS == nil {
		return Config{}, composectl.InitOptions{}, fmt.Errorf("https is required")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(config.AdminEmail))
	if err != nil || address.Address != strings.TrimSpace(config.AdminEmail) {
		return Config{}, composectl.InitOptions{}, fmt.Errorf("adminEmail must be a valid bare email address")
	}
	normalized, err := composectl.NormalizeInitOptions(composectl.InitOptions{
		AdminEmail: config.AdminEmail, Domain: config.Domain, Environment: config.Environment,
		Image: config.Image, NoHTTPS: !*config.HTTPS,
	})
	if err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	config.AdminEmail = normalized.AdminEmail
	config.Domain = normalized.Domain
	config.Environment = normalized.Environment
	config.Image = normalized.Image
	return config, normalized, nil
}

func readMarker(path string) (*Config, error) {
	contents, err := securefs.ReadPrivateFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read host installation marker: %w", err)
	}
	var config Config
	if err := json.Unmarshal(contents, &config); err != nil {
		return nil, fmt.Errorf("parse host installation marker: %w", err)
	}
	return &config, nil
}

func configsEqual(first, second Config) bool {
	if first.HTTPS == nil || second.HTTPS == nil {
		return first.HTTPS == nil && second.HTTPS == nil
	}
	return first.SchemaVersion == second.SchemaVersion && first.Domain == second.Domain &&
		first.AdminEmail == second.AdminEmail && first.Environment == second.Environment &&
		first.Image == second.Image && *first.HTTPS == *second.HTTPS
}
