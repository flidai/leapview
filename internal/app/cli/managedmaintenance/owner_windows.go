package managedmaintenance

import "os"

// Managed host execution is Linux-only; other builds retain the CLI help.
func operatorOwned(os.FileInfo) bool         { return false }
func trustedDirectoryOwner(os.FileInfo) bool { return false }
