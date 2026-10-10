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
	Name                       string `json:"name"`
	DuckDBVersion              string `json:"duckdbVersion"`
	Platform                   string `json:"platform"`
	EngineRevision             string `json:"engineRevision"`
	SourceRevision             string `json:"sourceRevision"`
	CargoLockSHA256            string `json:"cargoLockSHA256"`
	SourceArchiveSHA256        string `json:"sourceArchiveSHA256,omitempty"`
	SQLiteSourceID             string `json:"sqliteSourceID,omitempty"`
	SQLiteSourceSHA3           string `json:"sqliteSourceSHA3,omitempty"`
	SourcePatchSHA256          string `json:"sourcePatchSHA256,omitempty"`
	NativeDependencyLockSHA256 string `json:"nativeDependencyLockSHA256,omitempty"`
}

func CompiledBuiltin(name, platform string) (BuiltinDescriptor, bool) {
	if platform != "linux_amd64" && platform != "linux_arm64" {
		return BuiltinDescriptor{}, false
	}
	if staticVortexEnabled && name == "vortex" {
		return BuiltinDescriptor{Name: "vortex", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision: "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision: "275ac230e1d9afd08926b6989ec2467f92fae6e3", CargoLockSHA256: "4502cbe4f9611bbbdb4990226937e1d027fad414b533589f9ceb92cb9dd4aeb0",
			NativeDependencyLockSHA256: "9ebe41827182c32ca8963df83c4cdaa4a85a7797d36d8f67587d736b3e65298e"}, true
	}
	if staticDeltaEnabled && name == "delta" {
		return BuiltinDescriptor{Name: "delta", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision: "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision: "45c40878601b54b4188b09e08732fe0d576ad222", CargoLockSHA256: "fa9ca48bf887c7982387c6f4d60b78e0981a36b41c59fa4f745fd0a9a057de36",
			NativeDependencyLockSHA256: "6575f8fb88fcee4407b8ea5bf7ad9b9ce8de0480dbeb69a003b3f365ee2c55a3"}, true
	}
	if staticAzureEnabled && name == "azure" {
		return BuiltinDescriptor{Name: "azure", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision: "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision: "563589b2f24290a4dcdd4247eaedf2b544f9dbcd", NativeDependencyLockSHA256: "c4c2f1e57c8d863efd45a3d50fdfcad18e0936f564f717c62fc8f5cf98ae9513"}, true
	}
	if staticAvroEnabled && name == "avro" {
		return BuiltinDescriptor{Name: "avro", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision: "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision: "f9d590297485f0318f480372c70bdd852826e258", NativeDependencyLockSHA256: "ec030207b7daef63badc5cd21a176c1e3f08b33216c3c04f72c3046b08e31e52"}, true
	}
	if staticExcelEnabled && name == "excel" {
		return BuiltinDescriptor{Name: "excel", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision:             "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision:             "f4c72b5ef04a03b3a78a95b5a2ee94ba93e3178d",
			NativeDependencyLockSHA256: "20f4d69e10de2afd01ff7d533c3c37c4db612790c6f019bd45c8b3ac020f7ed9"}, true
	}
	if staticDatabaseEnabled && (name == "postgres" || name == "mysql") {
		revision := "8f813f9b9c9e52a9074a050a0be60f49160a6baa"
		if name == "mysql" {
			revision = "37006e53a58ddc31eeb96ff95c21f3196e27fcf2"
		}
		return BuiltinDescriptor{Name: name, DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision: "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision: revision, NativeDependencyLockSHA256: "0d46534db82fe7b73e088e7daa17d727a48874b845db22d7673bf3725b5e123d"}, true
	}
	if staticHTTPEnabled && (name == "httpfs" || name == "quack") {
		revision := "c3f215ab360f04dc3d3d5305fa81849c0121f111"
		if name == "quack" {
			revision = "40de7badae4193c29d9c0834473fb76acc6c51e6"
		}
		return BuiltinDescriptor{Name: name, DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision: "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision: revision, NativeDependencyLockSHA256: "dbf9eca1180f163a24aa928b3cc27afac2ce788546bc2ae066944e79f8b336d1"}, true
	}
	if staticDuckLakeEnabled && name == "ducklake" {
		return BuiltinDescriptor{Name: "ducklake", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision:             "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision:             "d318a545571d7d46eb751fa2aa5f6f4389285d3c",
			NativeDependencyLockSHA256: "23de3cc5a3cfc32bf7c054abbc89e4feec9fbf40dee4a3f1519bfcabd607935e"}, true
	}
	if staticSQLiteEnabled && name == "sqlite" {
		return BuiltinDescriptor{Name: "sqlite", DuckDBVersion: "v1.5.4", Platform: platform,
			EngineRevision:      "08e34c447bae34eaee3723cac61f2878b6bdf787",
			SourceRevision:      "494e9feed54c20b6bbfb665baf26864bc7e3b517",
			SourceArchiveSHA256: "1e71ddf93849c6a6ecf58b827c0692073d2dd7ee40196158068f7b29f422e87d",
			SQLiteSourceID:      "2026-07-24 19:02:57 bf7c7f30031888f4e796e429ab3978879485813aaca6f641c7b33e4e09459bcc",
			SQLiteSourceSHA3:    "67f423e9ebbbdc473cbc4772c872ee6b89f31fde4ed0279a5c25d5f65c043a16"}, true
	}
	if !staticLanceEnabled || name != "lance" {
		return BuiltinDescriptor{}, false
	}
	return BuiltinDescriptor{Name: "lance", DuckDBVersion: "v1.5.4", Platform: platform,
		EngineRevision:    "08e34c447bae34eaee3723cac61f2878b6bdf787",
		SourceRevision:    "350060612087e1138ffa1bbb11a535013558241a",
		CargoLockSHA256:   "e69a51c14dab6a9b7c412c763fdb6a5e1fe0089fd5199f944a482d4ced2b32ca",
		SourcePatchSHA256: "f1c3261a7b59a1890af0ae0b457cf0ebf1614508cd039cec6d390df0218d7388"}, true
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
	if b.Name == "ducklake" || b.Name == "httpfs" || b.Name == "quack" || b.Name == "postgres" || b.Name == "mysql" || b.Name == "excel" || b.Name == "avro" || b.Name == "delta" || b.Name == "azure" || b.Name == "vortex" {
		return "compiled:" + b.EngineRevision + ":" + b.SourceRevision + ":" + b.NativeDependencyLockSHA256
	}
	if b.Name == "sqlite" {
		return "compiled:" + b.EngineRevision + ":" + b.SourceRevision + ":" + b.SourceArchiveSHA256 + ":" + b.SQLiteSourceSHA3
	}
	return "compiled:" + b.EngineRevision + ":" + b.SourceRevision + ":" + b.CargoLockSHA256 + ":" + b.SourcePatchSHA256
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
