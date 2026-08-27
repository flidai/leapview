package composectl

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

type qualificationInspectionTarEntry struct {
	name     string
	mode     int64
	typeflag byte
	linkname string
	contents string
}

func TestQualificationCopyFromContainerExtractsHardenedHardlinks(t *testing.T) {
	entries := []qualificationInspectionTarEntry{
		{name: "leapview", mode: 0o700, typeflag: tar.TypeDir},
		{name: "leapview/home", mode: 0o550, typeflag: tar.TypeDir},
		{name: "leapview/home/managed-data", mode: 0o550, typeflag: tar.TypeDir},
		{name: "leapview/home/managed-data/blobs", mode: 0o550, typeflag: tar.TypeDir},
		{name: "leapview/home/managed-data/blobs/orders.csv", mode: 0o440, typeflag: tar.TypeReg, contents: "order_id\n1\n"},
		{name: "leapview/home/managed-data/revisions", mode: 0o550, typeflag: tar.TypeDir},
		{name: "leapview/home/managed-data/revisions/orders.csv", mode: 0o440, typeflag: tar.TypeLink, linkname: "leapview/home/managed-data/blobs/orders.csv"},
	}
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe",
		qualificationExecutor: qualificationInspectionArchiveExecutor(t, entries),
	})
	require.NoError(t, err)

	root, cleanup, err := controller.qualificationCopyFromContainer(
		t.Context(), "leapview-app", "/var/lib/leapview",
	)
	require.NoError(t, err)
	temporaryRoot := filepath.Dir(filepath.Dir(root))

	homeInfo, err := os.Stat(filepath.Join(root, "home"))
	require.NoError(t, err)
	if got, want := homeInfo.Mode().Perm(), os.FileMode(0o550); got != want {
		t.Fatalf("extracted home mode = %o, want %o", got, want)
	}
	blobInfo, err := os.Stat(filepath.Join(root, "home", "managed-data", "blobs", "orders.csv"))
	require.NoError(t, err)
	revisionInfo, err := os.Stat(filepath.Join(root, "home", "managed-data", "revisions", "orders.csv"))
	require.NoError(t, err)
	if got, want := blobInfo.Mode().Perm(), os.FileMode(0o440); got != want {
		t.Fatalf("extracted file mode = %o, want %o", got, want)
	}
	if !os.SameFile(blobInfo, revisionInfo) {
		t.Fatal("extracted managed-data revision is not hard-linked to its blob")
	}
	cleanup()
	_, err = os.Stat(temporaryRoot)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestQualificationCopyFromContainerPreservesContainedSymlink(t *testing.T) {
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe",
		qualificationExecutor: qualificationInspectionArchiveExecutor(
			t,
			[]qualificationInspectionTarEntry{{
				name:     "libstdc++.so.6",
				mode:     0o777,
				typeflag: tar.TypeSymlink,
				linkname: "libstdc++.so.6.0.33",
			}},
		),
	})
	require.NoError(t, err)

	localPath, cleanup, err := controller.qualificationCopyFromContainer(
		t.Context(), "leapview-app", "/var/lib/leapview",
	)
	require.NoError(t, err)
	defer cleanup()

	info, err := os.Lstat(localPath)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	linkname, err := os.Readlink(localPath)
	require.NoError(t, err)
	require.Equal(t, "libstdc++.so.6.0.33", linkname)
}

func TestQualificationCopyFromContainerRejectsUnsafeArchiveEntries(t *testing.T) {
	for _, entry := range []qualificationInspectionTarEntry{
		{name: "../escape", mode: 0o600, typeflag: tar.TypeReg, contents: "unsafe"},
		{name: "leapview/link", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		{name: "leapview/link", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "../../escape"},
		{name: "leapview/link", mode: 0o600, typeflag: tar.TypeLink, linkname: "../escape"},
	} {
		t.Run(entry.name+string(entry.typeflag), func(t *testing.T) {
			controller, err := New(Options{
				Root: t.TempDir(), DockerBin: "docker-probe",
				qualificationExecutor: qualificationInspectionArchiveExecutor(
					t, []qualificationInspectionTarEntry{entry},
				),
			})
			require.NoError(t, err)

			_, cleanup, err := controller.qualificationCopyFromContainer(
				t.Context(), "leapview-app", "/var/lib/leapview",
			)
			cleanup()
			require.Error(t, err)
		})
	}
}

func qualificationInspectionArchiveExecutor(
	t *testing.T,
	entries []qualificationInspectionTarEntry,
) qualificationExecutorFunc {
	t.Helper()
	return func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		if !slices.Equal(request.Arguments, []string{
			"cp", "leapview-app:/var/lib/leapview", "-",
		}) {
			t.Fatalf("Docker arguments = %v", request.Arguments)
		}
		if request.Stdout == nil {
			t.Fatal("container archive command did not stream stdout")
		}
		writer := tar.NewWriter(request.Stdout)
		for _, entry := range entries {
			header := &tar.Header{
				Name: entry.name, Mode: entry.mode,
				Typeflag: entry.typeflag, Linkname: entry.linkname,
				Size: int64(len(entry.contents)),
			}
			if err := writer.WriteHeader(header); err != nil {
				return nil, err
			}
			if entry.typeflag == tar.TypeReg {
				if _, err := io.WriteString(writer, entry.contents); err != nil {
					return nil, err
				}
			}
		}
		return nil, writer.Close()
	}
}
