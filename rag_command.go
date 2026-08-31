//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

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
	knowledgeJSON     bool
	knowledgeCitation string
	knowledgeSources  []string
	knowledgeForce    bool
	knowledgeStoreDir string
)

// registerRAGCommand registers the user-facing knowledge command and its
// subcommands, which build and inspect the local knowledge index. The agent's
// knowledge tools are not CLI commands; the CLI only builds and inspects the
// index. Every subcommand prints the canonical tier line so it is never ambiguous
// which tier is active.
func registerRAGCommand(cmd *fisk.Application) {
	k := cmd.Command("knowledge", "Builds and inspects the local knowledge base the agent searches").Alias("rag").Alias("k")
	// Not ExistingFileVar: it validates the default too, so a bare "fisk knowledge"
	// fails on a missing agent.yaml instead of printing the subcommand help. The file
	// is read by whichever subcommand runs, which reports a missing one itself.
	k.Flag("config", "Path to the agent configuration file").Default("agent.yaml").StringVar(&configFile)
	// The store base must match the one the agent runs with (agent.Options.StoreDir),
	// or the CLI writes the index to a different directory than the agent reads it from.
	// It is distinct from --state-dir, which locates session state, not the knowledge
	// index. An absolute knowledge directory in the config needs neither.
	k.Flag("store-dir", "Base directory the knowledge index resolves under; must match the agent's store_dir (absolute)").Envar("FISK_AI_STORE_DIR").StringVar(&knowledgeStoreDir)

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
	search.Flag("json", "Render the result as a single JSON object for scripting; chunk bodies are carried only with --full").UnNegatableBoolVar(&knowledgeJSON)

	registerRAGMatchCommand(k)

	show := k.Command("show", "Prints one chunk verbatim, resolving a citation").Action(knowledgeShowAction)
	show.Arg("citation", "A citation token of the form <relpath>#<ordinal>").Required().StringVar(&knowledgeCitation)

	rm := k.Command("rm", "Removes specific indexed sources by path, as listed by knowledge sources").Action(knowledgeRmAction)
	rm.Arg("sources", "Source paths to remove, e.g. docs/design.md").Required().StringsVar(&knowledgeSources)

	reset := k.Command("reset", "Wipes the entire knowledge index").Action(knowledgeResetAction)
	reset.Flag("force", "Perform the wipe; without it, reset only reports what would be deleted").UnNegatableBoolVar(&knowledgeForce)

	k.Command("sources", "Lists indexed files with chunk counts and last-indexed time").Action(knowledgeSourcesAction)

	k.Command("doctor", "Checks the index and, when configured, the embeddings server").Action(knowledgeDoctorAction)
	k.Command("rebuild", "Rebuilds the search index from the stored text, without re-embedding").Action(knowledgeRebuildAction)
	k.Command("stats", "Prints the tier banner and index counts and sizes").Action(knowledgeStatsAction)

	registerKnowledgeWordsCommand(k)
	registerKnowledgeAgentCommand(k)
}

// knowledgeConfig parses the config in the lenient MCP mode (the knowledge CLI
// inspects a configuration without running the agent, so it needs neither a prompt
// nor a model) and confirms RAG is enabled.
func knowledgeConfig() (*config.Config, error) {
	if knowledgeStoreDir != "" && !filepath.IsAbs(knowledgeStoreDir) {
		return nil, fmt.Errorf("--store-dir must be an absolute path, got %q", knowledgeStoreDir)
	}

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

	store, err := rag.OpenWriter(cfg, knowledgeStoreDir)
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
	}

	// With the vector tier on, an offline dry pass (it embeds nothing) says how many
	// chunks the real run will embed: the cost preview for a first build, and the
	// total the progress bar counts up to.
	var bar *indexBar
	if !knowledgeDryRun && cfg.RAGVectorEnabled() {
		total, err := estimateEmbeddings(ctx, store, roots, opts)
		if errors.Is(err, context.Canceled) {
			indexCanceled(false)
			return nil
		}
		if err != nil {
			return err
		}
		bar = newIndexBar(total)
	}
	// Covers the error return below: fisk prints that error after this function
	// returns, and a live render loop would erase it.
	defer bar.stop()

	opts.Progress = func(msg string) { bar.note(msg) }
	opts.OnFile = func(ev rag.IndexEvent) { bar.advance(ev.Embeddings) }

	stats, err := store.Index(ctx, roots, opts)
	if errors.Is(err, context.Canceled) {
		// The index is incremental by content hash, so the files embedded before
		// the interrupt are committed and re-running skips them; say so rather than
		// dumping a raw cancellation error or exiting silently. The bar is left where
		// it stopped, which is the honest answer to how far the run got.
		barShown := bar != nil
		bar.stop()
		indexCanceled(barShown)

		return nil
	}
	if err != nil {
		return err
	}

	bar.done()
	bar.stop()
	printIndexStats(stats, knowledgeDryRun)

	return nil
}

// indexCanceled reports an interrupted index run. The newline separates the message
// from the echoed interrupt, which a rendered bar has already ended.
func indexCanceled(barShown bool) {
	if barShown {
		fmt.Fprintln(os.Stderr, "index canceled; already-indexed files are skipped on re-run")
		return
	}

	fmt.Fprintln(os.Stderr, "\nindex canceled; already-indexed files are skipped on re-run")
}

// estimateEmbeddings runs an offline dry pass and returns how many chunks the real
// run will embed, printing the cost preview when this is a first full build or a
// reindex, so a large embedding job is never a surprise. It returns zero when
// nothing would consume the answer, rather than walking the corpus for no reader.
func estimateEmbeddings(ctx context.Context, store *rag.Store, roots []string, opts rag.IndexOptions) (int, error) {
	st, err := store.Stats(ctx)
	if err != nil {
		return 0, err
	}

	firstBuild := st.Documents == 0 || opts.Reindex
	if !firstBuild && !util.StdoutIsTerminal() {
		return 0, nil
	}

	dry := opts
	dry.DryRun = true
	dry.Progress = nil
	dry.OnFile = nil
	est, err := store.Index(ctx, roots, dry)
	if err != nil {
		return 0, err
	}

	if firstBuild {
		fmt.Printf("first full build: about to embed %d chunks across %d files; run with --dry-run to preview\n",
			est.Embeddings, est.Files)
	}

	return est.Embeddings, nil
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

	store, err := rag.Open(cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}
	defer store.Close()

	res, err := store.Search(ctx, knowledgeQuery, knowledgeTopK)
	if err != nil {
		return err
	}

	// Before the document is built: every soft outcome below is a status field in
	// the JSON rather than a printed line, so a consumer tells "no index" from "no
	// results" without matching on prose, and the exit stays 0 for both.
	if knowledgeJSON {
		return writeRAGJSON(newRAGSearchJSON(knowledgeQuery, res, store.VectorEnabled(), knowledgeFull))
	}

	c := columns.New()
	defer c.WriteTo(os.Stdout)

	if res.Degraded {
		c.Print(rag.DegradedTierLine(res.DegradeKind, res.DegradeReason))
	} else if err := printTierLine(ctx, c, store); err != nil {
		return err
	}

	switch res.Status {
	case rag.StatusIndexNotBuilt:
		c.Print("the knowledge index has not been built yet; run: fisk knowledge index")
		return nil
	case rag.StatusIndexEmpty:
		c.Print("the knowledge index is empty, or the query had no searchable terms")
		return nil
	}

	// A user staring at a search that found nothing is precisely the user who needs
	// the other question, and this line is the cheapest discovery surface there is.
	if len(res.Hits) == 0 {
		c.Print("no results")
		c.Blank()
		c.Print(matchSuggestion(knowledgeQuery))
		return nil
	}

	renderSearchHits(c, res.Hits, knowledgeFull)

	return nil
}

// renderSearchHits adds one section per hit.
//
// The raw citation stays the section heading so it can be pasted straight into
// knowledge show, which accepts only that token: a citation rule is a regular
// expression and is not reversible, so a published URL resolves back to no chunk.
// The mapped citation is a field under it, and only when a rule matched, because a
// line repeating the path is noise on a corpus that is mostly unpublished.
//
// A citation carries a corpus path and a mapped citation can carry the document's
// own heading, so both are sanitized on the way out.
func renderSearchHits(c *columns.Document, hits []rag.Hit, full bool) {
	for _, h := range hits {
		c.Section(terminalToken(h.Citation), func(c *columns.Document) {
			if h.Mapped {
				c.Item("Mapped", terminalToken(h.MappedCitation))
			}
			c.ItemUnlessZero("Section", h.HeadingPath)
			if full {
				c.Item("Chunk", h.Content)
			} else {
				c.Item("Chunk", util.TruncateLine(h.Content, 100))
			}
		})
	}
}

// terminalToken sanitizes a raw or a mapped citation for a terminal without
// cutting it short. The operator pastes a raw citation into knowledge show and a
// mapped one into a browser or wherever the rules publish to, and neither accepts
// a token that lost its tail. terminalText truncates, since a table cell has a
// width.
func terminalToken(s string) string {
	return util.SanitizeForTerminal(s, utf8.RuneCountInString(s))
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

	store, err := rag.Open(cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}
	defer store.Close()

	headingPath, content, err := store.ChunkText(ctx, relPath, ordinal)
	if errors.Is(err, rag.ErrIndexNotBuilt) {
		return fmt.Errorf("the knowledge index has not been built yet; run: fisk knowledge index")
	}
	if err != nil {
		return fmt.Errorf("no chunk found for citation %q: it may have shifted since the last reindex; run 'fisk knowledge sources' to list files", knowledgeCitation)
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

	exists, err := rag.StoreExists(cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Println("the knowledge index has not been built yet; run: fisk knowledge index")
		return nil
	}

	store, err := rag.OpenWriter(cfg, knowledgeStoreDir)
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

	exists, err := rag.StoreExists(cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Println("no knowledge index to reset")
		return nil
	}

	store, err := rag.OpenWriter(cfg, knowledgeStoreDir)
	// An index from another format generation cannot be opened, so its rows can be
	// neither counted nor cleared, and discarding the file is the whole of the fix.
	// Reset is the command that does that, so it answers for itself here rather than
	// passing on an error that would tell the operator to run reset.
	if errors.Is(err, rag.ErrFormatTooOld) {
		if !knowledgeForce {
			return fmt.Errorf("knowledge reset would discard the index at %s, which this build cannot read; re-run with --force to confirm",
				rag.StorePath(cfg, knowledgeStoreDir))
		}

		path, destroyErr := rag.Destroy(cfg, knowledgeStoreDir)
		if destroyErr != nil {
			return destroyErr
		}

		fmt.Printf("reset: discarded %s, which was built by an older format and could not be read\n", path)
		fmt.Println("run: fisk knowledge index")

		return nil
	}
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

	err = store.Reset(ctx)
	// The reset is one transaction, so an interrupt rolls it back and the index is
	// still there. Say so: this is the command that deletes the corpus, and a bare
	// cancellation error leaves the operator unable to tell whether it ran.
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "\nreset canceled; the index is unchanged")
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Printf("reset: removed %d documents and %d chunks from %s\n", st.Documents, st.Chunks, st.StorePath)

	return nil
}

// knowledgeRebuildAction repairs a search index that has drifted from the stored
// text, which is the state knowledge doctor --integrity reports.
//
// It is its own verb rather than a repair offered by the doctor because it is not
// diagnosis: given a damaged chunks table it will build a consistent index over the
// damage, after which the integrity check passes and reports nothing. That makes it
// something an operator chooses, having read what it does.
func knowledgeRebuildAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}
	defer store.Close()

	c := columns.New()
	defer c.WriteTo(os.Stdout)

	// Said before the work starts, because the reassuring half is the part an
	// operator staring at a corruption error needs: their documents are not at risk
	// and this does not cost another embedding run.
	c.Print("Rebuilding the search index from the stored document text.")
	c.Print("The documents, their text and any vectors are left untouched, so nothing is re-embedded.")
	c.Blank()

	if err := store.RebuildFTS(ctx); err != nil {
		switch {
		case errors.Is(err, rag.ErrIndexNotBuilt):
			return fmt.Errorf("there is no knowledge index to rebuild; run: fisk knowledge index")
		case errors.Is(err, rag.ErrLocked):
			return fmt.Errorf("another knowledge writer holds the index lock; rebuild needs it, so try again when it finishes")
		}

		return err
	}

	c.Print("Rebuilt. Verify with: fisk knowledge doctor --integrity")

	return nil
}

func knowledgeSourcesAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := printTierLine(ctx, nil, store); err != nil {
		return err
	}

	sources, err := store.Sources(ctx)
	if errors.Is(err, rag.ErrIndexNotBuilt) {
		fmt.Println("the knowledge index has not been built yet; run: fisk knowledge index")
		return nil
	}
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Println("no indexed files")
		return nil
	}

	// Without rules there is nothing to map and no rule that can fail to match, so
	// the column and the count are left off entirely rather than filling a listing
	// with blanks for the operators who never published their corpus.
	var mapper *rag.CitationMapper
	if len(cfg.RAGCitationRules()) > 0 {
		mapper = store.CitationMapper()
	}

	tbl, unmapped := sourcesTable(sources, mapper)
	if _, err := tbl.WriteTo(os.Stdout); err != nil {
		return err
	}

	if mapper != nil {
		fmt.Printf("\n%d of %d %s matched no citation rule and %s cited by path\n",
			unmapped, len(sources), plural(len(sources), "document", "documents"), plural(unmapped, "is", "are"))
	}

	return nil
}

// sourcesTable lists the indexed documents and reports how many of them no
// citation rule matched.
//
// A nil mapper means no citation rules are configured, which leaves the mapped
// column off and the count at zero. Where rules are configured the column is blank
// for a document none of them matched, and the count says how many: a rule
// matching nothing sends raw paths to the model and reports no error anywhere.
//
// The mapped citation is the document-level one; see
// rag.CitationMapper.RenderDocument for how it differs from knowledge match.
func sourcesTable(sources []rag.Source, mapper *rag.CitationMapper) (*table.Table, int) {
	tbl := table.NewTableWriter("")

	if mapper == nil {
		tbl.AddHeaders("Path", "Chunks", "Last Indexed")
		for _, s := range sources {
			tbl.AddRow(s.Path, s.Chunks, s.MTime)
		}

		return tbl, 0
	}

	var unmapped int

	tbl.AddHeaders("Path", "Chunks", "Last Indexed", "Mapped")
	for _, s := range sources {
		mappedCitation, mapped := mapper.RenderDocument(s.Path)
		// A rule whose replacement is nothing but reserved names matches and then
		// renders empty at document level, and a document with an empty mapped
		// citation is unmapped whichever way it got there. Counting it as mapped
		// would report the corpus clean while every cell in the column was blank.
		if !mapped || mappedCitation == "" {
			unmapped++
			mappedCitation = ""
		}

		tbl.AddRow(s.Path, s.Chunks, s.MTime, terminalText(mappedCitation))
	}

	return tbl, unmapped
}

func knowledgeDoctorAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	c := columns.New()
	defer c.WriteTo(os.Stdout)

	// An index doctor cannot open is exactly what the operator ran doctor to learn
	// about, so a stale format or a mismatched embedding identity is reported as a
	// failed check carrying its own fix rather than returned as a bare error.
	store, err := rag.Open(cfg, knowledgeStoreDir)
	if err != nil {
		c.Item("Store readable", columns.Style(fmt.Sprintf("[%s] %v", doctorMark(rag.DoctorFail), err)))
		return fmt.Errorf("knowledge doctor found problems that must be fixed")
	}
	defer store.Close()

	report, err := store.Doctor(ctx, cfg.Harness.RAG.Paths)
	if err != nil {
		return err
	}

	c.Heading(report.TierLine)

	for _, check := range report.Checks {
		mark := doctorMark(check.State)
		if check.Detail != "" {
			c.Item(check.Name, columns.Style(fmt.Sprintf("[%s] %s", mark, check.Detail)))
		} else {
			c.Item(check.Name, columns.Style(fmt.Sprintf("[%s]", mark)))
		}
	}

	if report.HasUnrun() {
		c.Blank()
		c.Print("Some checks did not run, so this report verified less than it lists.")
	}

	if report.HasFatal() {
		return fmt.Errorf("knowledge doctor found problems that must be fixed")
	}

	return nil
}

// doctorMark renders a check state. A skipped check reads as neither a pass nor a
// failure, because it is neither and a reader scanning a column of marks will take
// whichever one it borrows at face value.
func doctorMark(state rag.DoctorState) string {
	switch state {
	case rag.DoctorPass:
		return " {green}ok{/green} "
	case rag.DoctorFail:
		return "{red}FAIL{/red}"
	default:
		return "{yellow}skip{/yellow}"
	}
}

func knowledgeStatsAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := knowledgeConfig()
	if err != nil {
		return err
	}

	store, err := rag.Open(cfg, knowledgeStoreDir)
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
		c.Item("Store", fmt.Sprintf("%s (not built; run: fisk knowledge index)", st.StorePath))
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
