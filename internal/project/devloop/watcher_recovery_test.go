package devloop

import (
	"context"
	"errors"
	"io/fs"
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
	updates, events, _ := runFilesystemWatcher(t, root)
	initial := awaitUpdate(t, updates)
	require.Equal(t, StatusSynchronized, initial.Result.Status)
	require.NoError(t, initial.Err)

	// Initial success is reported before source resolution finishes. Witness
	// the event loop so this edit cannot instead fail that initial resolution,
	// which reports its error with the previous synchronized result.
	ready := filepath.Join(root, "ready-for-invalid-edit.txt")
	require.NoError(t, os.WriteFile(ready, []byte("not a resource\n"), 0o600))
	awaitWatcherEvent(t, events, ready)

	resource := filepath.Join(root, "connections", "customer.yaml")
	writeWatcherResource(t, resource, "not a resource envelope\n")
	invalid := awaitWatcherInvalidation(t, updates, initial.Result)
	require.Equal(t, StatusInvalid, invalid.Result.Status)
	require.Error(t, invalid.Err)
	require.Equal(t, initial.Result.Candidate, invalid.Result.Candidate)
	require.Equal(t, initial.Result.Snapshot, invalid.Result.Snapshot)

	writeWatcherResource(t, resource, watcherConnection("customer"))
	recovered := awaitWatcherSnapshot(t, updates, "connections/customer.yaml", watcherConnection("customer"))
	require.Equal(t, StatusSynchronized, recovered.Result.Status)
	require.NotEqual(t, initial.Result.Candidate.ArtifactDigest, recovered.Result.Candidate.ArtifactDigest)
}

func TestWatcherReinstallsRemovedAndRecreatedDirectory(t *testing.T) {
	root := t.TempDir()
	writeWatcherResource(t, filepath.Join(root, "connections", "warehouse.yaml"), watcherConnection("warehouse"))
	resource := filepath.Join(root, "connections", "team", "customer.yaml")
	writeWatcherResource(t, resource, watcherConnection("customer"))
	updates, _, _ := runFilesystemWatcher(t, root)
	require.Equal(t, StatusSynchronized, awaitUpdate(t, updates).Result.Status)

	require.NoError(t, os.RemoveAll(filepath.Dir(resource)))
	removed := awaitWatcherSnapshot(t, updates, "connections/warehouse.yaml", watcherConnection("warehouse"))
	require.Len(t, removed.Result.Snapshot.Artifacts, 1)

	writeWatcherResource(t, resource, watcherConnection("customer"))
	restored := awaitWatcherSnapshot(t, updates, "connections/team/customer.yaml", watcherConnection("customer"))
	require.Len(t, restored.Result.Snapshot.Artifacts, 2)
	updated := watcherConnection("customer") + "# edit after recreation\n"
	writeWatcherResource(t, resource, updated)
	_ = awaitWatcherSnapshot(t, updates, "connections/team/customer.yaml", updated)
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
			// Removing an authored tree may race an in-flight directory scan.
			// The watcher reports that transient state before reconciling the
			// queued removal. Still require the final valid snapshot below.
			if errors.Is(update.Err, fs.ErrNotExist) {
				continue
			}
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

func awaitWatcherInvalidation(t *testing.T, updates <-chan Update, previous Result) Update {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case update := <-updates:
			require.Error(t, update.Err)
			require.Equal(t, previous.Candidate, update.Result.Candidate)
			require.Equal(t, previous.Snapshot, update.Result.Snapshot)
			if update.Result.Status == StatusInvalid {
				return update
			}
			// Run reports a successful reconcile before resolving its source
			// watches. An edit during that resolution can first report a scan
			// error with the previous result, before the queued edit is built.
			require.Equal(t, StatusSynchronized, update.Result.Status)
		case <-deadline.C:
			t.Fatal("watcher did not report an invalid resource after a filesystem change")
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
	done   chan struct{}
}

func observeFilesystemEvents(source watchSource, observed chan<- fileEvent) *observedFilesystemSource {
	wrapped := &observedFilesystemSource{watchSource: source, events: make(chan fileEvent), closed: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(wrapped.done)
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
	err := source.watchSource.Close()
	<-source.done
	return err
}
