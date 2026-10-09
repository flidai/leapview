package duckdbsession

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flidai/leapview/internal/extension"
)

// VerifyCompiledBuiltin verifies the engine's actual install mode before the
// fixed compiled name can be loaded. A file-backed extension can never satisfy
// this check, and automatic acquisition is disabled by the caller's verifier.
func VerifyCompiledBuiltin(ctx context.Context, db *sql.DB, identity extension.Identity) error {
	if err := extension.ValidateBuiltinIdentity(identity); err != nil {
		return err
	}
	for _, statement := range []string{"SET autoinstall_known_extensions = false", "SET autoload_known_extensions = false"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: disable builtin acquisition", extension.ErrExtensionIntegrity)
		}
	}
	var installed bool
	var mode, path string
	engineName := extension.ArtifactFilenameStem(identity.Name)
	if err := db.QueryRowContext(ctx, "SELECT installed, install_mode, install_path FROM duckdb_extensions() WHERE extension_name = ?", engineName).Scan(&installed, &mode, &path); err != nil {
		return fmt.Errorf("%w: builtin registry status unavailable", extension.ErrExtensionIntegrity)
	}
	if !installed || mode != "STATICALLY_LINKED" || path != "(BUILT-IN)" {
		return fmt.Errorf("%w: extension is not statically linked into this engine", extension.ErrExtensionIntegrity)
	}
	// ValidateBuiltinIdentity limits this literal to the closed compiled registry.
	if _, err := db.ExecContext(ctx, "LOAD "+engineName); err != nil {
		return fmt.Errorf("%w: load compiled builtin", extension.ErrExtensionIntegrity)
	}
	var loaded bool
	if err := db.QueryRowContext(ctx, "SELECT loaded FROM duckdb_extensions() WHERE extension_name = ?", engineName).Scan(&loaded); err != nil || !loaded {
		return fmt.Errorf("%w: compiled builtin did not load", extension.ErrExtensionIntegrity)
	}
	return nil
}
