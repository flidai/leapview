package main

// typeSpecWorkspaceEntry excludes dependency installations and mutable workspace
// state without excluding ordinary authored hidden directories.
func typeSpecWorkspaceEntry(name string) bool {
	switch name {
	case ".git", ".tmp", ".cache", ".worktrees", ".artifacts", "node_modules":
		return true
	default:
		return false
	}
}
