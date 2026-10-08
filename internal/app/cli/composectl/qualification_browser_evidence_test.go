//go:build linux

package composectl

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationBrowserEvidenceDefaultPathIsUnchanged(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	cleanup := &qualificationCleanup{}
	path, err := prepareQualificationBrowserEvidence(qualificationAuthoringOptions{EvidenceDir: root}, cleanup)
	require.NoError(t, err)
	require.Equal(t, root, path)
	require.NoError(t, cleanup.Run(t.Context()))
	_, err = os.Stat(root)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestQualificationBrowserEvidenceOwnsOnlyPrivateChildAndRestoresAfterRemoval(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	before, err := os.Stat(root)
	require.NoError(t, err)
	owner := before.Sys().(*syscall.Stat_t)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	cleanup := &qualificationCleanup{}
	path, err := privateQualificationBrowserEvidence(root, uid, gid, cleanup)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "browser"), path)
	child, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), child.Mode().Perm())
	require.Equal(t, uint32(uid), child.Sys().(*syscall.Stat_t).Uid)
	parent, err := os.Stat(root)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), parent.Mode().Perm())
	require.Equal(t, owner.Uid, parent.Sys().(*syscall.Stat_t).Uid)
	require.Equal(t, owner.Gid, parent.Sys().(*syscall.Stat_t).Gid)
	removed := false
	cleanup.Add(func(context.Context) error {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, uint32(uid), info.Sys().(*syscall.Stat_t).Uid, "browser removal precedes ownership restoration")
		removed = true
		return nil
	})
	require.NoError(t, cleanup.Run(t.Context()))
	require.True(t, removed)
	restored, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, owner.Uid, restored.Sys().(*syscall.Stat_t).Uid)
	require.Equal(t, owner.Gid, restored.Sys().(*syscall.Stat_t).Gid)
	require.Equal(t, os.FileMode(0o700), restored.Mode().Perm())
	// Moving browser diagnostics beneath their own mount must not bypass the
	// existing recursive evidence scan before public report publication.
	require.NoError(t, os.WriteFile(filepath.Join(path, "failure.json"), []byte(`{"error":"private-browser-secret"}`), 0o600))
	require.ErrorContains(t, qualificationEvidenceExcludesSecrets(root, []string{"private-browser-secret"}), "temporary credential")
}

func TestQualificationBrowserEvidenceRejectsPublicSymlinkAndExistingDirectories(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o755))
	_, err := privateQualificationBrowserEvidence(root, os.Getuid(), os.Getgid(), &qualificationCleanup{})
	require.ErrorContains(t, err, "0700")
	require.NoError(t, os.Chmod(root, 0o700))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(root, link))
	_, err = privateQualificationBrowserEvidence(link, os.Getuid(), os.Getgid(), &qualificationCleanup{})
	require.ErrorContains(t, err, "0700")
	child := filepath.Join(root, "browser")
	require.NoError(t, os.Mkdir(child, 0o700))
	_, err = privateQualificationBrowserEvidence(root, os.Getuid(), os.Getgid(), &qualificationCleanup{})
	require.ErrorIs(t, err, os.ErrExist)
}
