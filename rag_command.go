//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/choria-io/fisk"
	"github.com/choria-io/ui/columns"
	"github.com/choria-io/ui/table"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/util"
)

var (
	knowledgePaths    []string
	knowledgeReindex  bool
	knowledgeDryRun   bool
	knowledgeQuery    string
	knowledgeTopK     int
	knowledgeFull     bool
	knowledgeCitation string
	knowledgeSources  []string
	knowledgeForce    bool
)

// registerRAGCommand registers the user-facing knowledge command and its
// subcommands, which build and inspect the local knowledge index. The agent's
// knowledge_search tool is not a CLI command; the CLI only builds and inspects the
// index. Every subcommand prints the canonical tier line so it is never ambiguous
// which tier is active.
func registerRAGCommand(cmd *fisk.Application) {
	k := cmd.Command("knowledge", "Builds and inspects the local knowledge base for the knowledge_search tool").Alias("rag").Alias("k")
	k.Flag("config", "Path to the agent configuration file").Default("agent.yaml").ExistingFileVar(&configFile)

	idx := k.Command("index", "Builds or updates the index (incremental by content hash)").Action(knowledgeIndexAction)
	idx.Arg("paths", "Paths to index; defaults to knowledge.paths from the config").StringsVar(&knowledgePaths)
	idx.Flag("reindex", "Force a full rebuild, dropping and re-embedding everything (also allows a model or dimension change)").UnNegatableBoolVar(&knowledgeReindex)
	idx.Flag("dry-run", "List the files and estimate the chunk and embedding-call counts without writing or embedding anything").UnNegatableBoolVar(&knowledgeDryRun)

	watch := k.Command("watch", "Watches the knowledge paths and re-indexes on change").Action(knowledgeWatchAction)
	watch.Arg("paths", "Paths to watch; defaults to knowledge.paths from the config").StringsVar(&knowledgePaths)
	watch.Flag("debounce", "How long to wait for changes to settle before re-indexing").Default("2s").DurationVar(&knowledgeWatchDebounce)
	watch.Flag("no-initial", "Skip the initial index pass and only watch for later changes").UnNegatableBoolVar(&knowledgeWatchNoInitial)

	search := k.Command("search", "Retrieves from the index for tuning; prints citations and snippets").Action(knowledgeSearchAction)
	search.Arg("query", "The search query").Required().StringVar(&knowledgeQuery)
	search.Flag("top-k", "Maximum number of results to return").IntVar(&knowledgeTopK)
	search.Flag("full", "Print the full chunk content instead of a snippet").UnNegatableBoolVar(&knowledgeFull)

	show := k.Command("show", "Prints one chunk verbatim, resolving a citation").Action(knowledgeShowAction)
	show.Arg("citation", "A citation token of the form <relpath>#<ordinal>").Required().StringVar(&knowledgeCitation)

	rm := k.Command("rm", "Removes specific indexed sources by path, as listed by knowledge sources").Action(knowledgeRmAction)
	rm.Arg("sources", "Source paths to remove, e.g. docs/design.md").Required().StringsVar(&knowledgeSources)

	reset := k.Command("reset", "Wipes the entire knowledge index").Action(knowledgeResetAction)
	reset.Flag("force", "Perform the wipe; without it, reset only reports what would be deleted").UnNegatableBoolVar(&knowledgeForce)

	k.Command("sources", "Lists indexed files with chunk counts and last-indexed time").Action(knowledgeSourcesAction)
	k.Command("doctor", "Checks the index and, when configured, the embeddings server").Action(knowledgeDoctorAction)
	k.Command("stats", "Prints the tier banner and index counts and sizes").Action(knowledgeStatsAction)
}

// knowledgeConfig parses the config in the lenient MCP mode (the knowledge CLI
// inspects a configuration without running the agent, so it needs neither a prompt
// nor a model) and confirms RAG is enabled.
func knowledgeConfig() (*config.Config, error) {
	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeMCP)
	if err != nil {
		return nil, err
	}
	if !cfg.RAGEnabled() {
		return nil, fmt.Errorf("knowledge is not enabled in %q; add a harness.knowledge block with 'enabled: true'", configFile)
	}

	return cfg, nil
}

// printTierLine prints the canonical tier line for a store to stdout.
func printTierLine(ctx context.Context, c *columns.Document, store *rag.Store) error {
	line, err := store.TierLine(ctx)
	if err != nil {
		return err
	}

	if c == nil {
		fmt.Println(line)
	} else {
		c.Print(line)
	}

	return nil
}

func knowledgeIndexAction(_ *fisk.ParseContext) error {
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

	store, err := rag.OpenWriter(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := printTierLine(ctx, nil, store); err != nil {
		return err
	}

	opts := rag.IndexOptions{
		Reindex:   knowledgeReindex,
		DryRun:    knowledgeDryRun,
		Reconcile: reconcile,
		Progress:  func(msg string) { fmt.Println(msg) },
	}

	// On a first full build (or a reindex) with the vector tier on, preview the
	// embedding cost the operator has no intuition for. The preview is an offline
	// dry pass (it embeds nothing); the real run follows.
	if !knowledgeDryRun && cfg.RAGVectorEnabled() {
		if err := previewFirstBuild(ctx, store, roots, opts); err != nil {
			return err
		}
	}

	stats, err := store.Index(ctx, roots, opts)
	if errors.Is(err, context.Canceled) {
		// The index is incremental by content hash, so the files embedded before
		// the interrupt are committed and re-running skips them; say so rather than
		// dumping a raw cancellation error or exiting silently.
		fmt.Fprintln(os.Stderr, "\nindex canceled; already-indexed files are skipped on re-run")
		return nil
	}
	if err != nil {
		return err
	}

	printIndexStats(stats, knowledgeDryRun)

	return nil
}

// previewFirstBuild prints an embedding-cost estimate before the first full build
// or a reindex, so a large embedding job is never a surprise.
func previewFirstBuild(ctx context.Context, store *rag.Store, roots []string, opts rag.IndexOptions) error {
	st, err := store.Stats(ctx)
	if err != nil {
		return err
	}
	if st.Documents != 0 && !opts.Reindex {
		return nil
	}

	dry := opts
	dry.DryRun = true
	dry.Progress = nil
	est, err := store.Index(ctx, roots, dry)
	if err != nil {
		return err
	}

	fmt.Printf("first full build: about to embed %d chunks across %d files; run with --dry-run to preview\n",
		est.Embeddings, est.Files)

	return nil
}

// printIndexStats prints the outcome of an index run.
func printIndexStats(stats *rag.IndexStats, dryRun bool) {
	verb := "indexed"
	if dryRun {
		verb = "would index"
	}
	fmt.Printf("%s: added=%d updated=%d skipped=%d removed=%d (%d files, %d chunks",
		verb, stats.Added, stats.Updated, stats.Skipped, stats.Removed, stats.Files, stats.Chunks)
	if stats.Embeddings > 0 {
		fmt.Printf(", %d embeddings", stats.Embeddings)
	}
	fmt.Println(")")
}

func knowledgeSearchAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	res, err := store.Search(ctx, knowledgeQuery, knowledgeTopK)
	if err != nil {
		return err
	}

	c := columns.New()
	defer c.WriteTo(os.Stdout)

	if res.Degraded {
		c.Print(rag.DegradedTierLine(res.DegradeReason))
	} else if err := printTierLine(ctx, c, store); err != nil {
		return err
	}

	switch res.Status {
	case rag.StatusIndexNotBuilt:
		c.Print("the knowledge index has not been built yet; run: fisk-ai knowledge index")
		return nil
	case rag.StatusIndexEmpty:
		c.Print("the knowledge index is empty, or the query had no searchable terms")
		return nil
	}

	if len(res.Hits) == 0 {
		c.Print("no results")
		return nil
	}

	for _, h := range res.Hits {
		c.Section(h.Citation, func(c *columns.Document) {
			c.ItemUnlessZero("Section", h.HeadingPath)
			if knowledgeFull {
				c.Item("Chunk", h.Content)
			} else {
				c.Item("Chunk", util.TruncateLine(h.Content, 100))
			}
		})
	}

	return nil
}

func knowledgeShowAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	relPath, ordinal, err := parseCitation(knowledgeCitation)
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	headingPath, content, err := store.ChunkText(ctx, relPath, ordinal)
	if errors.Is(err, rag.ErrIndexNotBuilt) {
		return fmt.Errorf("the knowledge index has not been built yet; run: fisk-ai knowledge index")
	}
	if err != nil {
		return fmt.Errorf("no chunk found for citation %q: it may have shifted since the last reindex; run 'fisk-ai knowledge sources' to list files", knowledgeCitation)
	}

	if headingPath != "" {
		fmt.Printf("# %s\n\n", headingPath)
	}
	fmt.Println(content)

	return nil
}

func knowledgeRmAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	exists, err := rag.StoreExists(cfg)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Println("the knowledge index has not been built yet; run: fisk-ai knowledge index")
		return nil
	}

	store, err := rag.OpenWriter(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := printTierLine(ctx, nil, store); err != nil {
		return err
	}

	var removed int
	for _, src := range knowledgeSources {
		ok, err := store.DeleteDocument(ctx, src)
		if err != nil {
			return err
		}
		if ok {
			removed++
			fmt.Printf("removed %s\n", src)
		} else {
			fmt.Printf("not indexed: %s\n", src)
		}
	}

	fmt.Printf("removed %d of %d sources\n", removed, len(knowledgeSources))

	return nil
}

func knowledgeResetAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	exists, err := rag.StoreExists(cfg)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Println("no knowledge index to reset")
		return nil
	}

	store, err := rag.OpenWriter(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	st, err := store.Stats(ctx)
	if err != nil {
		return err
	}

	if !knowledgeForce {
		return fmt.Errorf("knowledge reset would delete %d documents and %d chunks from %s; re-run with --force to confirm",
			st.Documents, st.Chunks, st.StorePath)
	}

	if err := store.Reset(ctx); err != nil {
		return err
	}

	fmt.Printf("reset: removed %d documents and %d chunks from %s\n", st.Documents, st.Chunks, st.StorePath)

	return nil
}

func knowledgeSourcesAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := printTierLine(ctx, nil, store); err != nil {
		return err
	}

	sources, err := store.Sources(ctx)
	if errors.Is(err, rag.ErrIndexNotBuilt) {
		fmt.Println("the knowledge index has not been built yet; run: fisk-ai knowledge index")
		return nil
	}
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Println("no indexed files")
		return nil
	}

	tbl := table.NewTableWriter("")
	defer tbl.WriteTo(os.Stdout)

	tbl.AddHeaders("Path", "Chunks", "Last Indexed")
	for _, s := range sources {
		tbl.AddRow(s.Path, s.Chunks, s.MTime)
	}

	return nil
}

func knowledgeDoctorAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	report, err := store.Doctor(ctx, cfg.Harness.RAG.Paths)
	if err != nil {
		return err
	}

	c := columns.New()
	defer c.WriteTo(os.Stdout)

	c.Heading(report.TierLine)

	for _, check := range report.Checks {
		mark := " {green}ok{/green} "
		if !check.OK {
			mark = "{red}FAIL{/red}"
		}
		if check.Detail != "" {
			c.Item(check.Name, columns.Style(fmt.Sprintf("[%s] %s", mark, check.Detail)))
		} else {
			c.Item(check.Name, columns.Style(fmt.Sprintf("[%s]", mark)))
		}
	}

	if report.HasFatal() {
		return fmt.Errorf("knowledge doctor found problems that must be fixed")
	}

	return nil
}

func knowledgeStatsAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	c := columns.New()
	defer c.WriteTo(os.Stdout)

	if err := printTierLine(ctx, c, store); err != nil {
		return err
	}

	st, err := store.Stats(ctx)
	if err != nil {
		return err
	}

	c.Blank()

	if !st.Built {
		c.Item("Store", fmt.Sprintf("%s (not built; run: fisk-ai knowledge index)", st.StorePath))
		return nil
	}

	c.Item("Store", st.StorePath)
	c.Item("Documents", st.Documents)
	c.Item("Chunks", st.Chunks)
	c.Item("Vectors", st.Vectors)
	if st.VectorTier {
		c.Item("Model", st.Meta.Model)
		c.Item("Dimension", st.Meta.Dimension)
		c.Item("Normalized", st.Meta.Normalized)
	}
	c.Item("DB size", columns.IBytes(st.DBSize))
	c.Item("WAL size", columns.IBytes(st.WALSize))
	c.ItemUnlessZero("Modified", st.LastModified)

	return nil
}

// parseCitation splits a <relpath>#<ordinal> citation into its path and ordinal,
// erroring on a malformed token so knowledge show reports it clearly.
func parseCitation(citation string) (string, int, error) {
	idx := strings.LastIndex(citation, "#")
	if idx < 0 {
		return "", 0, fmt.Errorf("citation %q is missing the '#<ordinal>' suffix; expected <relpath>#<ordinal>", citation)
	}
	relPath := citation[:idx]
	ordinal, err := strconv.Atoi(citation[idx+1:])
	if relPath == "" || err != nil || ordinal < 0 {
		return "", 0, fmt.Errorf("citation %q is malformed; expected <relpath>#<ordinal>, e.g. docs/design.md#3", citation)
	}

	return relPath, ordinal, nil
}
