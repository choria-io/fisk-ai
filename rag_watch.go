//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/internal/rag"
)

var (
	knowledgeWatchDebounce  time.Duration
	knowledgeWatchNoInitial bool
)

func knowledgeWatchAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	roots := knowledgePaths
	reconcile := false
	if len(roots) == 0 {
		roots = cfg.Harness.RAG.Paths
		reconcile = true // a full-corpus walk over the configured paths reconciles deletions
	}
	if len(roots) == 0 {
		return fmt.Errorf("no paths given and knowledge.paths is empty - pass a path or set knowledge.paths")
	}

	// Print the tier line from a read-only store so it never contends with the writer
	// locks the watcher takes for each index pass.
	rstore, err := rag.Open(cfg)
	if err != nil {
		return err
	}
	err = printTierLine(ctx, nil, rstore)
	rstore.Close()
	if err != nil {
		return err
	}

	watcher, err := rag.NewWatcher(cfg, rag.WatchOptions{
		Roots:       roots,
		Debounce:    knowledgeWatchDebounce,
		Reconcile:   reconcile,
		SkipInitial: knowledgeWatchNoInitial,
	}, watchReporter(roots))
	if err != nil {
		return err
	}
	defer watcher.Close()

	err = watcher.Run(ctx)
	if errors.Is(err, rag.ErrLocked) {
		return fmt.Errorf("another knowledge writer (index or watch) is already running")
	}
	if err != nil {
		return err
	}

	fmt.Println("stopped watching")

	return nil
}

// watchReporter wires the watcher's lifecycle hooks to the CLI's output, keeping
// re-index summaries identical to the index command and sending warnings and
// failures to stderr so a redirected stdout carries only stats.
func watchReporter(roots []string) rag.WatchReporter {
	return rag.WatchReporter{
		OnPlan: func(est *rag.IndexStats) {
			fmt.Printf("first full build: about to embed %d chunks across %d files; run knowledge index --dry-run to preview\n",
				est.Embeddings, est.Files)
		},
		OnStart: func(dirs int) {
			fmt.Printf("watching %d %s under %d %s (debounce %s); press Ctrl-C to stop\n",
				dirs, plural(dirs, "directory", "directories"),
				len(roots), plural(len(roots), "path", "paths"), knowledgeWatchDebounce)
		},
		OnDetect: func(paths []string) {
			if len(paths) == 0 {
				fmt.Printf("%s change detected; reindexing\n", watchStamp())
				return
			}
			fmt.Printf("%s change detected in %d %s; reindexing:\n", watchStamp(), len(paths), plural(len(paths), "file", "files"))
			for _, p := range paths {
				fmt.Printf("  %s\n", p)
			}
		},
		OnIndex: func(stats *rag.IndexStats, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s reindex failed: %v\n", watchStamp(), err)
				return
			}
			printIndexStats(stats, false)
		},
		OnWarn: func(msg string) {
			fmt.Fprintf(os.Stderr, "%s %s\n", watchStamp(), msg)
		},
	}
}

func watchStamp() string {
	return time.Now().Format("[15:04:05]")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}
