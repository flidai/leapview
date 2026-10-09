package extension

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// BuiltinDescriptor binds the separately admitted source-built engine to its
// static extension inputs. It never authorizes loading a file or a new name.
type BuiltinDescriptor struct {
	Name            string `json:"name"`
	DuckDBVersion   string `json:"duckdbVersion"`
	Platform        string `json:"platform"`
	EngineRevision  string `json:"engineRevision"`
	SourceRevision  string `json:"sourceRevision"`
	CargoLockSHA256 string `json:"cargoLockSHA256"`
}

func CompiledBuiltin(name, platform string) (BuiltinDescriptor, bool) {
	if !staticLanceEnabled || name != "lance" || (platform != "linux_amd64" && platform != "linux_arm64") {
		return BuiltinDescriptor{}, false
	}
	return BuiltinDescriptor{Name: "lance", DuckDBVersion: "v1.5.4", Platform: platform,
		EngineRevision:  "08e34c447bae34eaee3723cac61f2878b6bdf787",
		SourceRevision:  "350060612087e1138ffa1bbb11a535013558241a",
		CargoLockSHA256: "9d7e406bb9174769960775d7f75233b01e4a96a9bd9be8de3040be1badc91839"}, true
}

func (b BuiltinDescriptor) Bytes() []byte {
	payload, _ := json.Marshal(b)
	return append(payload, '\n')
}

func (b BuiltinDescriptor) Digest() string {
	hash := sha256.Sum256(b.Bytes())
	return "sha256:" + hex.EncodeToString(hash[:])
}

func (b BuiltinDescriptor) Provenance() string {
	return "compiled:" + b.EngineRevision + ":" + b.SourceRevision + ":" + b.CargoLockSHA256
}

// ValidateBuiltinIdentity requires the closed compile-time registry, rather
// than trusting a manifest's builtin flag or accepting arbitrary static names.
func ValidateBuiltinIdentity(identity Identity) error {
	builtin, ok := CompiledBuiltin(identity.Name, identity.Platform)
	arch := "amd64"
	if identity.Platform == "linux_arm64" {
		arch = "arm64"
	}
	if !ok || !identity.Builtin || identity.GOOS != "linux" || identity.GOARCH != arch || identity.DuckDBVersion != builtin.DuckDBVersion || identity.ExtensionVersion != builtin.SourceRevision || identity.Digest != builtin.Digest() {
		return fmt.Errorf("%w: builtin does not match the compiled engine inputs", ErrExtensionIntegrity)
	}
	return identity.Validate()
}

func VerifyBuiltinDescriptor(identity Identity, payload []byte, provenance, signature string) error {
	if err := ValidateBuiltinIdentity(identity); err != nil {
		return err
	}
	builtin, _ := CompiledBuiltin(identity.Name, identity.Platform)
	if !bytes.Equal(payload, builtin.Bytes()) || provenance != builtin.Provenance() || signature != "package:compiled-engine" {
		return fmt.Errorf("%w: builtin descriptor differs from the compiled registry", ErrExtensionIntegrity)
	}
	return nil
}
