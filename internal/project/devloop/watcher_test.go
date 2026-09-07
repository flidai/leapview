package devloop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWatcherDebouncesReachableChangesAndIgnoresUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	projectPath := root
	modelPath := filepath.Join(root, "models", "orders.yaml")
	builder := &countingBuilder{snapshot: testSnapshot("watch")}
	remote := &recordingRemote{}
	service, err := New(builder, remote)
	require.NoError(t, err)
	source := newFakeWatchSource()
	watcher, err := newWatcher(projectPath, service, watcherOptions{
		debounce:  10 * time.Millisecond,
		newSource: func() (watchSource, error) { return source, nil },
		resolveSources: func(string) ([]string, error) {
			return []string{projectPath, modelPath}, nil
		},
	})
	require.NoError(t, err)

	updates := make(chan Update, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, func(update Update) { updates <- update }) }()

	if update := awaitUpdate(t, updates); update.Result.Status != StatusSynchronized {
		t.Fatalf("initial update = %#v", update)
	}
	source.events <- fileEvent{name: filepath.Join(root, "notes.txt")}
	time.Sleep(30 * time.Millisecond)
	if got := builder.Calls(); got != 1 {
		t.Fatalf("builds after unrelated event = %d, want 1", got)
	}

	for range 5 {
		source.events <- fileEvent{name: modelPath}
	}
	if update := awaitUpdate(t, updates); update.Result.Status != StatusUnchanged {
		t.Fatalf("debounced update = %#v", update)
	}
	if got := builder.Calls(); got != 2 {
		t.Fatalf("builds after burst = %d, want 2", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatcherRecognizesNewlyReachableFileAndAddsItsDirectory(t *testing.T) {
	root := t.TempDir()
	projectPath := root
	modelPath := filepath.Join(root, "models", "orders.yaml")
	newPath := filepath.Join(root, "models", "customers.yaml")
	var sourcesMu sync.Mutex
	sources := []string{projectPath, modelPath}
	builder := &countingBuilder{snapshot: testSnapshot("new-file")}
	service, err := New(builder, &recordingRemote{})
	require.NoError(t, err)
	source := newFakeWatchSource()
	watcher, err := newWatcher(projectPath, service, watcherOptions{
		debounce:  10 * time.Millisecond,
		newSource: func() (watchSource, error) { return source, nil },
		resolveSources: func(string) ([]string, error) {
			sourcesMu.Lock()
			defer sourcesMu.Unlock()
			return append([]string(nil), sources...), nil
		},
	})
	require.NoError(t, err)

	updates := make(chan Update, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, func(update Update) { updates <- update }) }()
	_ = awaitUpdate(t, updates)

	sourcesMu.Lock()
	sources = append(sources, newPath)
	sourcesMu.Unlock()
	source.events <- fileEvent{name: newPath}
	if update := awaitUpdate(t, updates); update.Result.Status != StatusUnchanged {
		t.Fatalf("new reachable file update = %#v", update)
	}
	if !source.Added(filepath.Dir(newPath)) {
		t.Fatalf("new reachable directory was not watched: %#v", source.added)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatcherReceivesRealFilesystemChanges(t *testing.T) {
	root := t.TempDir()
	projectPath := root
	modelPath := filepath.Join(root, "models", "orders.yaml")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	builder := &countingBuilder{snapshot: testSnapshot("real-watch")}
	service, err := New(builder, &recordingRemote{})
	require.NoError(t, err)
	watcher, err := newWatcher(projectPath, service, watcherOptions{
		debounce:       10 * time.Millisecond,
		newSource:      newFSNotifySource,
		resolveSources: func(string) ([]string, error) { return []string{projectPath, modelPath}, nil },
	})
	require.NoError(t, err)

	updates := make(chan Update, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, func(update Update) { updates <- update }) }()
	_ = awaitUpdate(t, updates)

	if err := os.WriteFile(modelPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if update := awaitUpdate(t, updates); update.Result.Status != StatusUnchanged {
		t.Fatalf("filesystem update = %#v", update)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatcherRetriesRemoteFailureWithoutAnotherFileChange(t *testing.T) {
	root := t.TempDir()
	projectPath := root
	snapshot := testSnapshot("retry-watch")
	builder := &countingBuilder{snapshot: snapshot}
	remote := &recordingRemote{errors: []error{errors.New("target disconnected"), nil}}
	service, err := New(builder, remote)
	require.NoError(t, err)
	source := newFakeWatchSource()
	watcher, err := newWatcher(projectPath, service, watcherOptions{
		debounce: 10 * time.Millisecond, retryMin: 10 * time.Millisecond, retryMax: 20 * time.Millisecond,
		newSource: func() (watchSource, error) { return source, nil },
		resolveSources: func(string) ([]string, error) {
			return []string{projectPath}, nil
		},
	})
	require.NoError(t, err)

	updates := make(chan Update, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, func(update Update) { updates <- update }) }()
	if update := awaitUpdate(t, updates); update.Result.Status != StatusRetryable {
		t.Fatalf("first update = %#v, want retryable", update)
	}
	if update := awaitUpdate(t, updates); update.Result.Status != StatusSynchronized {
		t.Fatalf("retried update = %#v, want synchronized", update)
	}
	if len(remote.requests) != 2 {
		t.Fatalf("remote requests = %d, want automatic retry", len(remote.requests))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func awaitUpdate(t *testing.T, updates <-chan Update) Update {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher update")
		return Update{}
	}
}

type countingBuilder struct {
	mu       sync.Mutex
	snapshot Snapshot
	calls    int
}

func (builder *countingBuilder) Build(context.Context) (Snapshot, error) {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	builder.calls++
	return builder.snapshot, nil
}

func (builder *countingBuilder) Calls() int {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	return builder.calls
}

type fakeWatchSource struct {
	events chan fileEvent
	errors chan error
	mu     sync.Mutex
	added  []string
}

func newFakeWatchSource() *fakeWatchSource {
	return &fakeWatchSource{
		events: make(chan fileEvent, 16),
		errors: make(chan error, 1),
	}
}

func (source *fakeWatchSource) Add(path string) error {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.added = append(source.added, path)
	return nil
}

func (source *fakeWatchSource) Events() <-chan fileEvent { return source.events }
func (source *fakeWatchSource) Errors() <-chan error     { return source.errors }
func (source *fakeWatchSource) Close() error             { return nil }

func (source *fakeWatchSource) Added(path string) bool {
	source.mu.Lock()
	defer source.mu.Unlock()
	for _, added := range source.added {
		if added == path {
			return true
		}
	}
	return false
}
