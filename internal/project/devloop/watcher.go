package devloop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
)

const (
	defaultDebounce = 150 * time.Millisecond
	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
)

// Update is one observable development-loop build attempt. Candidate contains
// the last valid remote candidate even when Err reports a new invalid edit.
type Update struct {
	Result Result
	Err    error
}

type Watcher struct {
	sourceRoot     string
	service        *Service
	debounce       time.Duration
	retryMin       time.Duration
	retryMax       time.Duration
	newSource      func() (watchSource, error)
	resolveSources func(string) ([]string, error)
}

func NewWatcher(sourceRoot string, service *Service) (*Watcher, error) {
	return newWatcher(sourceRoot, service, watcherOptions{
		debounce:       defaultDebounce,
		retryMin:       defaultRetryMin,
		retryMax:       defaultRetryMax,
		newSource:      newFSNotifySource,
		resolveSources: projectcompiler.SourceFiles,
	})
}

type watcherOptions struct {
	debounce       time.Duration
	retryMin       time.Duration
	retryMax       time.Duration
	newSource      func() (watchSource, error)
	resolveSources func(string) ([]string, error)
}

func newWatcher(sourceRoot string, service *Service, options watcherOptions) (*Watcher, error) {
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(sourceRoot); statErr == nil && !info.IsDir() {
		return nil, fmt.Errorf("Project authoring was removed; pass the analytics source root directory %q instead of %q", filepath.Dir(sourceRoot), sourceRoot)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(sourceRoot); resolveErr == nil {
		sourceRoot = resolved
	}
	if service == nil || options.debounce <= 0 ||
		options.newSource == nil || options.resolveSources == nil {
		return nil, fmt.Errorf("project watcher requires service, debounce, source, and resolver")
	}
	if options.retryMin == 0 {
		options.retryMin = defaultRetryMin
	}
	if options.retryMax == 0 {
		options.retryMax = defaultRetryMax
	}
	if options.retryMin < 0 || options.retryMax < options.retryMin {
		return nil, fmt.Errorf("project watcher retry bounds are invalid")
	}
	return &Watcher{
		sourceRoot:     filepath.Clean(sourceRoot),
		service:        service,
		debounce:       options.debounce,
		retryMin:       options.retryMin,
		retryMax:       options.retryMax,
		newSource:      options.newSource,
		resolveSources: options.resolveSources,
	}, nil
}

// Run performs an initial reconcile, then serializes debounced changes until
// context cancellation. Invalid edits are reported while the Service preserves
// the last valid candidate.
func (watcher *Watcher) Run(ctx context.Context, report func(Update)) error {
	if watcher == nil {
		return fmt.Errorf("project watcher is not configured")
	}
	if report == nil {
		report = func(Update) {}
	}
	source, err := watcher.newSource()
	if err != nil {
		return fmt.Errorf("create project file watcher: %w", err)
	}
	defer source.Close()
	events := source.Events()
	sourceErrors := source.Errors()

	tracked := make(map[string]struct{})
	watchedDirectories := map[string]struct{}{watcher.sourceRoot: {}}
	if err := source.Add(watcher.sourceRoot); err != nil {
		return fmt.Errorf("watch analytics source root: %w", err)
	}
	refreshDirectories := func() error {
		directories, err := projectcompiler.SourceDirectories(watcher.sourceRoot)
		if err != nil {
			return err
		}
		// Successfully resolved dashboard fragments may live outside the six
		// authored directories. Retain only their already known parent watches.
		for path := range tracked {
			directory := filepath.Dir(path)
			if path != watcher.sourceRoot {
				if info, statErr := os.Lstat(directory); statErr == nil && info.IsDir() {
					directories = append(directories, directory)
				}
			}
		}
		next := make(map[string]struct{}, len(directories))
		for _, directory := range directories {
			next[directory] = struct{}{}
			if _, exists := watchedDirectories[directory]; exists {
				continue
			}
			if err := source.Add(directory); err != nil {
				return fmt.Errorf("watch project source directory %q: %w", directory, err)
			}
			watchedDirectories[directory] = struct{}{}
		}
		for directory := range watchedDirectories {
			if _, exists := next[directory]; !exists {
				if err := source.Remove(directory); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
					return fmt.Errorf("remove project source directory watch %q: %w", directory, err)
				}
				delete(watchedDirectories, directory)
			}
		}
		return nil
	}
	if err := refreshDirectories(); err != nil {
		return fmt.Errorf("watch analytics source directories: %w", err)
	}
	installSources := func(paths []string) error {
		next := make(map[string]struct{}, len(paths))
		for _, path := range paths {
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			path = filepath.Clean(path)
			relative, relativeErr := filepath.Rel(watcher.sourceRoot, path)
			if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("project source %q is outside the analytics source root", path)
			}
			next[path] = struct{}{}
			if path == watcher.sourceRoot {
				continue
			}
			directory := filepath.Dir(path)
			if _, exists := watchedDirectories[directory]; exists {
				continue
			}
			if err := source.Add(directory); err != nil {
				return fmt.Errorf("watch project source directory %q: %w", directory, err)
			}
			watchedDirectories[directory] = struct{}{}
		}
		tracked = next
		return nil
	}
	resolveAndInstall := func() error {
		paths, err := watcher.resolveSources(watcher.sourceRoot)
		if err != nil {
			return err
		}
		return installSources(paths)
	}
	// Directory watches do not depend on successful compilation. Reconcile
	// reports invalid resources while these watches keep their repairs visible.
	_ = resolveAndInstall()

	var timer *time.Timer
	var timerC <-chan time.Time
	schedule := func(delay time.Duration) {
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		}
		timerC = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	var lastResult Result
	retryDelay := watcher.retryMin
	reconcile := func() {
		if refreshErr := refreshDirectories(); refreshErr != nil {
			report(Update{Result: lastResult, Err: refreshErr})
		}
		result, reconcileErr := watcher.service.Reconcile(ctx)
		lastResult = result
		report(Update{Result: result, Err: reconcileErr})
		if reconcileErr == nil {
			retryDelay = watcher.retryMin
			if refreshErr := resolveAndInstall(); refreshErr != nil {
				report(Update{Result: result, Err: refreshErr})
			}
			return
		}
		if result.Status == StatusRetryable {
			schedule(retryDelay)
			retryDelay = min(retryDelay*2, watcher.retryMax)
		}
	}
	reconcile()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, open := <-events:
			if !open {
				events = nil
				if sourceErrors == nil {
					return fmt.Errorf("project file watcher closed")
				}
				continue
			}
			eventPath, err := filepath.Abs(event.name)
			if err != nil {
				continue
			}
			eventPath = filepath.Clean(eventPath)
			relative, relativeErr := filepath.Rel(watcher.sourceRoot, eventPath)
			if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			_, directoryEvent := watchedDirectories[eventPath]
			if info, statErr := os.Lstat(eventPath); statErr == nil && info.IsDir() && projectcompiler.IsAuthoredSourcePath(relative) {
				directoryEvent = true
			}
			if directoryEvent {
				// fsnotify removes watches on deleted/moved directories. Invalidate
				// the old path even if an editor has already recreated it.
				if event.operation&(fsnotify.Remove|fsnotify.Rename) != 0 {
					for directory := range watchedDirectories {
						if directory == eventPath || strings.HasPrefix(directory, eventPath+string(filepath.Separator)) {
							if removeErr := source.Remove(directory); removeErr != nil && !errors.Is(removeErr, fsnotify.ErrNonExistentWatch) {
								report(Update{Result: lastResult, Err: removeErr})
							}
							delete(watchedDirectories, directory)
						}
					}
				}
				if refreshErr := refreshDirectories(); refreshErr != nil {
					report(Update{Result: lastResult, Err: refreshErr})
				}
			}
			_, relevant := tracked[eventPath]
			extension := strings.ToLower(filepath.Ext(eventPath))
			authoredYAML := (extension == ".yaml" || extension == ".yml") && projectcompiler.IsAuthoredSourcePath(relative)
			legacyManifest := relative == "leapview.yaml" || relative == "leapview.yml"
			if !relevant && !directoryEvent && !authoredYAML && !legacyManifest {
				// A dashboard include glob may gain a new fragment in an
				// already watched directory outside the six resource roots.
				// Only a successful compiler resolution establishes ownership.
				if extension != ".yaml" && extension != ".yml" {
					continue
				}
				paths, resolveErr := watcher.resolveSources(watcher.sourceRoot)
				if resolveErr != nil {
					continue
				}
				for _, path := range paths {
					absolute, absoluteErr := filepath.Abs(path)
					if absoluteErr == nil && filepath.Clean(absolute) == eventPath {
						relevant = true
						break
					}
				}
				if !relevant {
					continue
				}
				if installErr := installSources(paths); installErr != nil {
					report(Update{Result: lastResult, Err: installErr})
					continue
				}
			}
			retryDelay = watcher.retryMin
			schedule(watcher.debounce)
		case watchErr, open := <-sourceErrors:
			if !open {
				sourceErrors = nil
				if events == nil {
					return fmt.Errorf("project file watcher closed")
				}
				continue
			}
			if watchErr != nil {
				return fmt.Errorf("project file watcher: %w", watchErr)
			}
		case <-timerC:
			timerC = nil
			reconcile()
		}
	}
}

type fileEvent struct {
	name      string
	operation fsnotify.Op
}

type watchSource interface {
	Add(string) error
	Remove(string) error
	Events() <-chan fileEvent
	Errors() <-chan error
	Close() error
}

type fsNotifySource struct {
	watcher *fsnotify.Watcher
	events  chan fileEvent
	errors  chan error
	done    chan struct{}
	once    sync.Once
}

func newFSNotifySource() (watchSource, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	source := &fsNotifySource{
		watcher: watcher,
		events:  make(chan fileEvent),
		errors:  make(chan error),
		done:    make(chan struct{}),
	}
	go source.forward()
	return source, nil
}

func (source *fsNotifySource) Add(path string) error    { return source.watcher.Add(path) }
func (source *fsNotifySource) Remove(path string) error { return source.watcher.Remove(path) }
func (source *fsNotifySource) Events() <-chan fileEvent { return source.events }
func (source *fsNotifySource) Errors() <-chan error     { return source.errors }
func (source *fsNotifySource) Close() error {
	var err error
	source.once.Do(func() {
		close(source.done)
		err = source.watcher.Close()
	})
	return err
}

func (source *fsNotifySource) forward() {
	defer close(source.events)
	defer close(source.errors)
	for {
		select {
		case <-source.done:
			return
		case event, open := <-source.watcher.Events:
			if !open {
				return
			}
			select {
			case source.events <- fileEvent{name: event.Name, operation: event.Op}:
			case <-source.done:
				return
			}
		case err, open := <-source.watcher.Errors:
			if !open {
				return
			}
			select {
			case source.errors <- err:
			case <-source.done:
				return
			}
		}
	}
}
