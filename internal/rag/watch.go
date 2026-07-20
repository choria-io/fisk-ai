//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/choria-io/fisk-ai/config"
)

const (
	// minWatchDebounce floors the debounce so a misconfigured tiny value cannot turn
	// an editor's save burst into one re-index per event.
	minWatchDebounce = 100 * time.Millisecond
)

// WatchOptions controls a Watcher.
type WatchOptions struct {
	// Roots are the files and directories to watch and index.
	Roots []string
	// Debounce is how long to wait after the last change before re-indexing. It is
	// floored at 100ms.
	Debounce time.Duration
	// Reconcile enables orphan deletion on the initial index pass. It is set only for
	// a full-corpus walk (paths taken from the config, not given explicitly), matching
	// the index command. Reactive passes never reconcile; they delete per event.
	Reconcile bool
	// SkipInitial skips the startup index pass and only watches for later changes.
	SkipInitial bool
}

// WatchReporter renders a Watcher's lifecycle events. The rag package owns the
// mechanism; the caller owns presentation. Every hook is optional.
type WatchReporter struct {
	// OnPlan reports a first-build embedding estimate before the initial pass runs,
	// so a large embedding job is never a surprise. Called only on a first build with
	// the vector tier on.
	OnPlan func(est *IndexStats)
	// OnStart reports that watching has begun, with the number of watched directories.
	OnStart func(watchedDirs int)
	// OnDetect reports a settled batch of changed paths just before a re-index.
	OnDetect func(paths []string)
	// OnIndex reports the outcome of an index pass (initial or reactive).
	OnIndex func(stats *IndexStats, err error)
	// OnWarn surfaces a non-fatal condition: a retry after a lock, a watch-descriptor
	// limit, a skipped path.
	OnWarn func(msg string)
}

// Watcher watches Roots and keeps the knowledge index in line with them, opening
// the writer store only for the brief moment each index pass runs so other writers
// are not blocked between changes.
type Watcher struct {
	cfg         *config.Config
	roots       []string
	debounce    time.Duration
	reconcile   bool
	skipInitial bool
	exts        map[string]bool
	storeDir    string
	reporter    WatchReporter

	fsw *fsnotify.Watcher
}

// NewWatcher validates opts and creates the underlying file watcher. It does not
// open the index or add any watches; Run does that.
func NewWatcher(cfg *config.Config, opts WatchOptions, r WatchReporter) (*Watcher, error) {
	if len(opts.Roots) == 0 {
		return nil, fmt.Errorf("watch requires at least one path")
	}
	if opts.Debounce < minWatchDebounce {
		return nil, fmt.Errorf("debounce must be at least %s", minWatchDebounce)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating file watcher: %w", err)
	}

	return &Watcher{
		cfg:         cfg,
		roots:       opts.Roots,
		debounce:    opts.Debounce,
		reconcile:   opts.Reconcile,
		skipInitial: opts.SkipInitial,
		exts:        DefaultExtensions,
		storeDir:    filepath.Clean(resolveDir(cfg)),
		reporter:    r,
		fsw:         fsw,
	}, nil
}

// Close releases the file watcher. It is safe to call after Run returns.
func (w *Watcher) Close() error {
	return w.fsw.Close()
}

// Run performs the optional initial index pass, adds watches, and then blocks
// reacting to changes until ctx is canceled. A clean shutdown (ctx canceled)
// returns nil; only a fatal setup or index error is returned.
func (w *Watcher) Run(ctx context.Context) error {
	roots, err := w.resolveRoots()
	if err != nil {
		return err
	}
	w.roots = roots

	if !w.skipInitial {
		if err := w.initialIndex(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}

	watched, err := w.addWatches()
	if err != nil {
		return err
	}
	if w.reporter.OnStart != nil {
		w.reporter.OnStart(watched)
	}

	return w.loop(ctx)
}

// resolveRoots stats each configured root, warning about and dropping any that are
// missing or unreadable rather than failing the whole watch: a configured path that
// does not exist yet is a warning, not a fatal error. It returns the roots that
// exist, erroring only when none remain.
func (w *Watcher) resolveRoots() ([]string, error) {
	var existing []string
	for _, root := range w.roots {
		_, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			w.warn(fmt.Sprintf("skipping missing path %q", root))
			continue
		}
		if err != nil {
			w.warn(fmt.Sprintf("skipping %q: %v", root, err))
			continue
		}
		existing = append(existing, root)
	}

	if len(existing) == 0 {
		return nil, fmt.Errorf("none of the configured knowledge paths exist")
	}

	return existing, nil
}

// initialIndex runs the startup pass: a first-build estimate when relevant, then a
// full incremental index reconciling deletions over the configured corpus.
func (w *Watcher) initialIndex(ctx context.Context) error {
	store, err := OpenWriter(w.cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	if w.reporter.OnPlan != nil && w.cfg.RAGVectorEnabled() {
		est, err := w.plan(ctx, store)
		if err != nil {
			return err
		}
		if est != nil {
			w.reporter.OnPlan(est)
		}
	}

	stats, err := store.Index(ctx, w.roots, IndexOptions{Reconcile: w.reconcile})
	if err != nil {
		return err
	}
	if w.reporter.OnIndex != nil {
		w.reporter.OnIndex(stats, nil)
	}

	return nil
}

// plan estimates the embedding work for a first build (an empty store) via an
// offline dry pass; it returns nil when the store already holds documents.
func (w *Watcher) plan(ctx context.Context, store *Store) (*IndexStats, error) {
	st, err := store.Stats(ctx)
	if err != nil {
		return nil, err
	}
	if st.Documents != 0 {
		return nil, nil
	}

	return store.Index(ctx, w.roots, IndexOptions{DryRun: true, Reconcile: w.reconcile})
}

// addWatches registers a watch for every eligible directory under the roots. A file
// root is watched through its parent directory. It returns the number of watched
// directories.
func (w *Watcher) addWatches() (int, error) {
	count := 0
	limitHit := false

	for _, root := range w.roots {
		info, err := os.Lstat(root)
		if err != nil {
			w.warn(fmt.Sprintf("skipping %q: %v", root, err))
			continue
		}

		if !info.IsDir() {
			if err := w.addDir(filepath.Dir(root), &count, &limitHit); err != nil {
				return count, err
			}
			continue
		}

		if err := w.addTree(root, &count, &limitHit); err != nil {
			return count, err
		}
	}

	return count, nil
}

// addTree walks a directory and watches every directory in it, applying the same
// skip rules as the index walk. Symlinked directories are not followed, matching
// the index walk.
func (w *Watcher) addTree(root string, count *int, limitHit *bool) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if shouldSkipDir(path, w.storeDir) {
			return filepath.SkipDir
		}

		return w.addDir(path, count, limitHit)
	})
}

// addDir adds one directory watch, translating the platform's watch-descriptor
// exhaustion into a single actionable warning rather than a fatal error, so the
// watcher keeps working for the directories it did manage to register.
func (w *Watcher) addDir(dir string, count *int, limitHit *bool) error {
	err := w.fsw.Add(dir)
	if err == nil {
		*count++
		return nil
	}
	if errors.Is(err, syscall.ENOSPC) {
		if !*limitHit {
			*limitHit = true
			w.warn("file watch limit reached; some directories are unwatched and changes there will be missed - raise fs.inotify.max_user_watches")
		}
		return nil
	}

	return fmt.Errorf("watching %q: %w", dir, err)
}

// indexResult carries an index pass outcome from the indexing goroutine back to the
// event loop.
type indexResult struct {
	stats *IndexStats
	err   error
}

// loop is the event loop. Indexing runs in a separate goroutine so a long pass never
// blocks event draining (which would overflow the kernel's watch queue), and a
// dirty flag coalesces changes that arrive during a pass into one follow-up run.
func (w *Watcher) loop(ctx context.Context) error {
	timer := time.NewTimer(w.debounce)
	if !timer.Stop() {
		<-timer.C
	}

	arm := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.debounce)
	}

	pending := map[string]bool{}
	deletes := map[string]bool{}
	done := make(chan indexResult, 1)
	running := false
	dirty := false

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if w.handleEvent(ev, pending, deletes) {
				arm()
			}

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.warn(fmt.Sprintf("watch error: %v", err))

		case <-timer.C:
			if running {
				dirty = true
				continue
			}
			running = true
			if w.reporter.OnDetect != nil {
				w.reporter.OnDetect(keysOf(pending))
			}
			delKeys := keysOf(deletes)
			pending = map[string]bool{}
			deletes = map[string]bool{}
			go func() { done <- w.indexOnce(ctx, delKeys) }()

		case res := <-done:
			running = false
			switch {
			case errors.Is(res.err, context.Canceled):
				return nil
			case errors.Is(res.err, ErrLocked):
				w.warn("another knowledge writer is running; will retry")
				dirty = true
			case w.reporter.OnIndex != nil:
				w.reporter.OnIndex(res.stats, res.err)
			}
			if dirty {
				dirty = false
				arm()
			}
		}
	}
}

// handleEvent records a change and reports whether it should arm the debounce timer.
// A directory create adds watches for the new subtree and fires so the next pass's
// full walk indexes any files created before the watch was in place.
func (w *Watcher) handleEvent(ev fsnotify.Event, pending, deletes map[string]bool) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}

	// Reject anything under the store's own directory: its WAL and lock sidecars are
	// written by the index pass itself, and reacting to them would be a feedback loop.
	if w.underStoreDir(ev.Name) {
		return false
	}

	if ev.Op&fsnotify.Create != 0 {
		info, err := os.Lstat(ev.Name)
		if err == nil && info.IsDir() {
			if shouldSkipDir(ev.Name, w.storeDir) {
				return false
			}
			count := 0
			limitHit := false
			if err := w.addTree(ev.Name, &count, &limitHit); err != nil {
				w.warn(fmt.Sprintf("watching new directory %q: %v", ev.Name, err))
			}
			return true
		}
	}

	if !w.indexable(ev.Name) {
		return false
	}

	key := filepath.ToSlash(ev.Name)
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		deletes[key] = true
	}
	if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		pending[key] = true
		delete(deletes, key)
	}

	return true
}

// indexOnce opens the writer, runs a non-reconciling incremental pass, then applies
// the pending deletions, opening and closing the store so the writer lock is held
// only for the pass. Deletions are stat-guarded so an editor's atomic save (a
// transient rename that Index has already re-added) does not drop a live file.
func (w *Watcher) indexOnce(ctx context.Context, delKeys []string) indexResult {
	store, err := OpenWriter(w.cfg)
	if err != nil {
		return indexResult{err: err}
	}
	defer store.Close()

	stats, err := store.Index(ctx, w.roots, IndexOptions{Reconcile: false})
	if err != nil {
		return indexResult{stats: stats, err: err}
	}

	for _, key := range delKeys {
		if _, statErr := os.Stat(filepath.FromSlash(key)); statErr == nil {
			continue
		}
		removed, err := store.DeleteDocument(ctx, key)
		if err != nil {
			return indexResult{stats: stats, err: err}
		}
		if removed {
			stats.Removed++
		}
	}

	return indexResult{stats: stats}
}

func (w *Watcher) indexable(path string) bool {
	return w.exts[strings.ToLower(filepath.Ext(path))]
}

func (w *Watcher) underStoreDir(path string) bool {
	p := filepath.Clean(path)

	return p == w.storeDir || strings.HasPrefix(p, w.storeDir+string(filepath.Separator))
}

func (w *Watcher) warn(msg string) {
	if w.reporter.OnWarn != nil {
		w.reporter.OnWarn(msg)
	}
}

func keysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}

	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
