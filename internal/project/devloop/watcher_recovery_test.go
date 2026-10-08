package devloop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/stretchr/testify/require"
)

func TestWatcherRepairsInitiallyInvalidNestedResource(t *testing.T) {
	root := t.TempDir()
	resource := filepath.Join(root, "connections", "team", "warehouse.yaml")
	writeWatcherResource(t, resource, "not a resource envelope\n")
	updates, _, remote := runFilesystemWatcher(t, root)
	initial := awaitUpdate(t, updates)
	require.Equal(t, StatusInvalid, initial.Result.Status)
	require.Error(t, initial.Err)
	require.Empty(t, initial.Result.Candidate.ID)
	require.Empty(t, remote.requests)

	writeWatcherResource(t, resource, watcherConnection("warehouse"))
	recovered := awaitWatcherSnapshot(t, updates, "connections/team/warehouse.yaml", watcherConnection("warehouse"))
	require.Equal(t, StatusSynchronized, recovered.Result.Status)
	require.NotEmpty(t, recovered.Result.Candidate.ID)
}

func TestWatcherDiscoversNewNestedDirectoryAndKeepsWatching(t *testing.T) {
	root := t.TempDir()
	baseline := filepath.Join(root, "connections", "warehouse.yaml")
	writeWatcherResource(t, baseline, watcherConnection("warehouse"))
	updates, events, _ := runFilesystemWatcher(t, root)
	initial := awaitUpdate(t, updates)
	require.Equal(t, StatusSynchronized, initial.Result.Status)
	require.NoError(t, initial.Err)

	directory := filepath.Join(root, "connections", "new-team")
	require.NoError(t, os.Mkdir(directory, 0o700))
	// Witness a real directory event before creating its resource. The watcher
	// must install the new directory even though it contains no reachable file.
	awaitWatcherEvent(t, events, directory)
	resource := filepath.Join(directory, "customer.yaml")
	writeWatcherResource(t, resource, watcherConnection("customer"))
	first := awaitWatcherSnapshot(t, updates, "connections/new-team/customer.yaml", watcherConnection("customer"))
	require.Equal(t, StatusSynchronized, first.Result.Status)
	require.NotEqual(t, initial.Result.Candidate.ArtifactDigest, first.Result.Candidate.ArtifactDigest)

	updated := watcherConnection("customer") + "# another coherent edit\n"
	writeWatcherResource(t, resource, updated)
	second := awaitWatcherSnapshot(t, updates, "connections/new-team/customer.yaml", updated)
	require.NotEqual(t, first.Result.Candidate.ArtifactDigest, second.Result.Candidate.ArtifactDigest)
}

func TestWatcherReportsInvalidNewResourceAndPreservesCandidate(t *testing.T) {
	root := t.TempDir()
	writeWatcherResource(t, filepath.Join(root, "connections", "warehouse.yaml"), watcherConnection("warehouse"))
	updates, _, _ := runFilesystemWatcher(t, root)
	initial := awaitUpdate(t, updates)
	require.Equal(t, StatusSynchronized, initial.Result.Status)
	require.NoError(t, initial.Err)

	resource := filepath.Join(root, "connections", "customer.yaml")
	writeWatcherResource(t, resource, "not a resource envelope\n")
	invalid := awaitUpdate(t, updates)
	require.Equal(t, StatusInvalid, invalid.Result.Status)
	require.Error(t, invalid.Err)
	require.Equal(t, initial.Result.Candidate, invalid.Result.Candidate)
	require.Equal(t, initial.Result.Snapshot, invalid.Result.Snapshot)

	writeWatcherResource(t, resource, watcherConnection("customer"))
	recovered := awaitWatcherSnapshot(t, updates, "connections/customer.yaml", watcherConnection("customer"))
	require.Equal(t, StatusSynchronized, recovered.Result.Status)
	require.NotEqual(t, initial.Result.Candidate.ArtifactDigest, recovered.Result.Candidate.ArtifactDigest)
}

func watcherConnection(name string) string {
	return "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:" + name + ", name: " + name + "}\nspec: {type: managed}\n"
}

func writeWatcherResource(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	temporary := path + ".tmp"
	require.NoError(t, os.WriteFile(temporary, []byte(content), 0o600))
	require.NoError(t, os.Rename(temporary, path))
}

func runFilesystemWatcher(t *testing.T, root string) (<-chan Update, <-chan fileEvent, *recordingRemote) {
	t.Helper()
	remote := &recordingRemote{}
	service, err := New(FilesystemBuilder{SourceRoot: root, ProjectID: "sales_project"}, remote)
	require.NoError(t, err)
	observed := make(chan fileEvent, 64)
	watcher, err := newWatcher(root, service, watcherOptions{
		debounce:       10 * time.Millisecond,
		resolveSources: projectcompiler.SourceFiles,
		newSource: func() (watchSource, error) {
			source, sourceErr := newFSNotifySource()
			if sourceErr != nil {
				return nil, sourceErr
			}
			return observeFilesystemEvents(source, observed), nil
		},
	})
	require.NoError(t, err)
	updates := make(chan Update, 64)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- watcher.Run(ctx, func(update Update) {
			select {
			case updates <- update:
			case <-ctx.Done():
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			require.NoError(t, runErr)
		case <-time.After(2 * time.Second):
			t.Error("watcher did not stop after cancellation")
		}
	})
	return updates, observed, remote
}

func awaitWatcherSnapshot(t *testing.T, updates <-chan Update, path, content string) Update {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case update := <-updates:
			require.NoError(t, update.Err)
			for _, artifact := range update.Result.Snapshot.Artifacts {
				if artifact.Path == path && string(artifact.Content) == content {
					return update
				}
			}
		case <-deadline.C:
			t.Fatalf("watcher did not synchronize %s after a filesystem change", path)
			return Update{}
		}
	}
}

func awaitWatcherEvent(t *testing.T, events <-chan fileEvent, path string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.name == path {
				return
			}
		case <-deadline.C:
			t.Fatalf("filesystem watcher did not observe %s", path)
		}
	}
}

type observedFilesystemSource struct {
	watchSource
	events chan fileEvent
	closed chan struct{}
}

func observeFilesystemEvents(source watchSource, observed chan<- fileEvent) *observedFilesystemSource {
	wrapped := &observedFilesystemSource{watchSource: source, events: make(chan fileEvent), closed: make(chan struct{})}
	go func() {
		defer close(wrapped.events)
		for {
			select {
			case event, open := <-source.Events():
				if !open {
					return
				}
				select {
				case wrapped.events <- event:
				case <-wrapped.closed:
					return
				}
				select {
				case observed <- event:
				case <-wrapped.closed:
					return
				}
			case <-wrapped.closed:
				return
			}
		}
	}()
	return wrapped
}

func (source *observedFilesystemSource) Events() <-chan fileEvent { return source.events }

func (source *observedFilesystemSource) Close() error {
	close(source.closed)
	return source.watchSource.Close()
}
