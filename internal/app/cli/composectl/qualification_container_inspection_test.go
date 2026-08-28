package composectl

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestQualificationContainerArchiveRejectsAbsoluteSymlinkTarget(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "payload")
	_, err := extractQualificationContainerArchive(
		qualificationInspectionArchive(t, []qualificationInspectionTarEntry{{
			name: "leapview/link", mode: 0o777,
			typeflag: tar.TypeSymlink, linkname: "/etc/passwd",
		}}),
		targetRoot,
	)
	require.ErrorContains(t, err, "target is empty or absolute")
}

func TestQualificationContainerArchiveRejectsTraversalSymlinkTarget(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "payload")
	_, err := extractQualificationContainerArchive(
		qualificationInspectionArchive(t, []qualificationInspectionTarEntry{{
			name: "leapview/link", mode: 0o777,
			typeflag: tar.TypeSymlink, linkname: "../../escape",
		}}),
		targetRoot,
	)
	require.ErrorContains(t, err, "target escapes extraction root")
}

func TestQualificationContainerArchiveDoesNotWriteThroughEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	targetRoot := filepath.Join(root, "payload")
	escaped := filepath.Join(root, "outside", "written.txt")
	_, err := extractQualificationContainerArchive(
		qualificationInspectionArchive(t, []qualificationInspectionTarEntry{
			{
				name: "leapview/link", mode: 0o777,
				typeflag: tar.TypeSymlink, linkname: "../../outside",
			},
			{
				name: "leapview/link/written.txt", mode: 0o600,
				typeflag: tar.TypeReg, contents: "must stay contained",
			},
		}),
		targetRoot,
	)
	require.Error(t, err)
	_, statErr := os.Lstat(escaped)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestQualificationContainerArchiveExtractionRemainsWithinTargetRoot(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "payload")
	extracted, err := extractQualificationContainerArchive(
		qualificationInspectionArchive(t, []qualificationInspectionTarEntry{
			{name: "leapview", mode: 0o700, typeflag: tar.TypeDir},
			{name: "leapview/bin", mode: 0o700, typeflag: tar.TypeDir},
			{
				name: "leapview/bin/libstdc++.so.6", mode: 0o777,
				typeflag: tar.TypeSymlink, linkname: "../lib/libstdc++.so.6.0.33",
			},
			{name: "leapview/lib", mode: 0o700, typeflag: tar.TypeDir},
			{
				name: "leapview/lib/libstdc++.so.6.0.33", mode: 0o500,
				typeflag: tar.TypeReg, contents: "qualified library",
			},
		}),
		targetRoot,
	)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(targetRoot, "leapview"), extracted)

	resolved, err := filepath.EvalSymlinks(filepath.Join(extracted, "bin", "libstdc++.so.6"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(extracted, "lib", "libstdc++.so.6.0.33"), resolved)
	relative, err := filepath.Rel(targetRoot, resolved)
	require.NoError(t, err)
	require.NotEqual(t, "..", relative)
	require.False(t, strings.HasPrefix(relative, ".."+string(filepath.Separator)))
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
		_, err := io.Copy(request.Stdout, qualificationInspectionArchive(t, entries))
		return nil, err
	}
}

func qualificationInspectionArchive(
	t *testing.T,
	entries []qualificationInspectionTarEntry,
) io.Reader {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode,
			Typeflag: entry.typeflag, Linkname: entry.linkname,
			Size: int64(len(entry.contents)),
		}
		require.NoError(t, writer.WriteHeader(header))
		if entry.typeflag == tar.TypeReg {
			_, err := io.WriteString(writer, entry.contents)
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	return bytes.NewReader(archive.Bytes())
}
