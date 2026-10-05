package hostinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const OperatorBootstrapSchemaVersion = 1

const maxOperatorBootstrapBytes = 1 << 20

// OperatorBootstrap is a private first-install input containing the provider's
// production PostgreSQL connections and canonical pool/evidence artifacts.
type OperatorBootstrap struct {
	SchemaVersion int                                 `json:"schemaVersion"`
	Postgres      composectl.FirstInstallPostgres     `json:"postgres"`
	PhysicalPool  composectl.FirstInstallPhysicalPool `json:"physicalPool"`
}

func readAndValidateOperatorBootstrap(path string) (OperatorBootstrap, composectl.FirstInstallOptions, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("read operator bootstrap configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("operator bootstrap configuration must be a private regular file")
	}
	if info.Size() > maxOperatorBootstrapBytes {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("operator bootstrap configuration exceeds the size limit")
	}
	contents, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("read operator bootstrap configuration: %w", err)
	}
	if len(contents) > maxOperatorBootstrapBytes {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("operator bootstrap configuration exceeds the size limit")
	}
	if err := rejectDuplicateJSONKeys(contents); err != nil {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("operator bootstrap configuration is not strict JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var bootstrap OperatorBootstrap
	if err := decoder.Decode(&bootstrap); err != nil {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("operator bootstrap configuration does not match the required schema")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("operator bootstrap configuration must contain exactly one JSON object")
	}
	if bootstrap.SchemaVersion != OperatorBootstrapSchemaVersion {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("unsupported operator bootstrap schemaVersion %d", bootstrap.SchemaVersion)
	}
	options := composectl.FirstInstallOptions{Postgres: bootstrap.Postgres, PhysicalPool: bootstrap.PhysicalPool}
	if err := options.Validate(); err != nil {
		return OperatorBootstrap{}, composectl.FirstInstallOptions{}, fmt.Errorf("validate operator bootstrap configuration: %w", err)
	}
	return bootstrap, options, nil
}

func rejectDuplicateJSONKeys(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("JSON object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("JSON object contains a duplicate key")
				}
				seen[key] = struct{}{}
				if err := readValue(); err != nil {
					return err
				}
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim('}') {
				return fmt.Errorf("JSON object is malformed")
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim(']') {
				return fmt.Errorf("JSON array is malformed")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		return nil
	}
	if err := readValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("JSON has trailing content")
	}
	return nil
}
