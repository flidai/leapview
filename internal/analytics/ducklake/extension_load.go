package ducklake

import "github.com/flidai/leapview/internal/extension"

func admittedExtensionLoadStatement(admitted extension.AdmittedExtension, requested string) (string, error) {
	if err := validateAdmittedExtension(admitted, requested); err != nil {
		return "", err
	}
	if admitted.Builtin {
		identity := extension.Identity{Builtin: true, Name: admitted.Name, DuckDBVersion: admitted.DuckDBVersion,
			ExtensionVersion: admitted.ExtensionVersion, GOOS: admitted.GOOS, GOARCH: admitted.GOARCH,
			Platform: admitted.Platform, Digest: admitted.Digest, SupportProfile: admitted.SupportProfile}
		if err := extension.ValidateBuiltinIdentity(identity); err != nil {
			return "", err
		}
		return "LOAD " + extension.ArtifactFilenameStem(admitted.Name), nil
	}
	return "LOAD '" + sqlLiteral(admitted.Path) + "'", nil
}
