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

func TestWatcherDiscoversNewSharedDashboardFragment(t *testing.T) {
	root := t.TempDir()
	writeWatcherResource(t, filepath.Join(root, "connections", "warehouse.yaml"), watcherConnection("warehouse"))
	writeWatcherResource(t, filepath.Join(root, "sources", "orders.yaml"), `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}
`)
	writeWatcherResource(t, filepath.Join(root, "models", "orders.yaml"), `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  definition: {type: direct, source: orders}
  entities: [{name: id, type: primary, fields: [id]}]
  grain: {entity: id}
  fields: [{name: id, datatype: String}]
`)
	writeWatcherResource(t, filepath.Join(root, "semantic-models", "sales.yaml"), `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  datasets:
  - name: orders
    model: orders_model
    metrics: [{name: order_count, type: simple, empty: zero, agg: count, field: id}]
`)
	writeWatcherResource(t, filepath.Join(root, "dashboards", "sales.yaml"), `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard}
spec:
  semanticModel: sales
  filters: []
  includes:
    pages: [../shared/*.yaml]
  visuals: []
  pages: []
`)
	first := filepath.Join(root, "shared", "overview.yaml")
	writeWatcherResource(t, first, "pages: [{id: overview, title: Overview, components: []}]\n")
	paths, err := projectcompiler.SourceFiles(root)
	require.NoError(t, err)
	require.Contains(t, paths, first, "compiler supports fragments outside the six resource directories")
	builder := &countingBuilder{snapshot: testSnapshot("shared-fragments")}
	service, err := New(builder, &recordingRemote{})
	require.NoError(t, err)
	observed := make(chan fileEvent, 64)
	watcher, err := newWatcher(root, service, watcherOptions{
		debounce: 10 * time.Millisecond,
		newSource: func() (watchSource, error) {
			source, err := newFSNotifySource()
			if err != nil {
				return nil, err
			}
			return observeFilesystemEvents(source, observed), nil
		},
		resolveSources: projectcompiler.SourceFiles,
	})
	require.NoError(t, err)
	updates := make(chan Update, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, func(update Update) { updates <- update }) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	require.NoError(t, awaitUpdate(t, updates).Err)
	// Witness the event loop after initial resolution has installed its source
	// set. Otherwise creating the fragment can race the initial resolver.
	unrelated := filepath.Join(root, "notes.txt")
	// This unrelated event only establishes readiness. An atomic scratch-file
	// rename can race the initial compiler snapshot and fail strict inspection
	// before the fragment journey even starts. Keep actual YAML edits atomic.
	require.NoError(t, os.WriteFile(unrelated, []byte("not a resource\n"), 0o600))
	awaitWatcherEvent(t, observed, unrelated)
	added := filepath.Join(root, "shared", "details.yaml")
	writeWatcherResource(t, added, "pages: [{id: details, title: Details, components: []}]\n")
	paths, err = projectcompiler.SourceFiles(root)
	require.NoError(t, err)
	require.Contains(t, paths, added)
	require.NoError(t, awaitUpdate(t, updates).Err)
	// Success is reported before the resolver finishes installing the new
	// source set. Witness the event loop again before the next atomic edit so
	// its scratch-file rename cannot race that post-reconcile snapshot. Use a
	// distinct path to avoid consuming a leftover event from the first probe.
	readyForEdit := filepath.Join(root, "notes-after-create.txt")
	require.NoError(t, os.WriteFile(readyForEdit, []byte("not a resource\n"), 0o600))
	awaitWatcherEvent(t, observed, readyForEdit)
	writeWatcherResource(t, added, "pages: [{id: details, title: Updated, components: []}]\n")
	require.NoError(t, awaitUpdate(t, updates).Err)
	require.GreaterOrEqual(t, builder.Calls(), 3)
}
