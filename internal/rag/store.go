//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package rag implements the local knowledge base behind the agent's built-in
// knowledge_search tool and the fisk-ai knowledge CLI. The whole index is a
// single SQLite file (documents, heading-delimited chunks, an FTS5 lexical index,
// and an optional sqlite-vec vector index) opened via the pure-Go, CGo-free
// modernc.org/sqlite driver. Retrieval is tiered: an always-on FTS5/BM25 lexical
// baseline, plus an opt-in vector tier fused with Reciprocal Rank Fusion when an
// embeddings server is configured.
//
// The user-facing surface is named "knowledge"; the Go identifiers keep the rag
// prefix because RAG is the technique. The store text is unencrypted on disk (file
// mode 0600), the same posture as the memory feature: do not index secrets.
package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	// The pure-Go SQLite driver registers the "sqlite" driver name; the vec
	// subpackage registers the vec0 virtual-table module. Both are blank-imported
	// so a lexical-only build still links the one binary; the vec0 table is only
	// ever created when the vector tier is on.
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/choria-io/fisk-ai/config"
)

const (
	// formatVersion is the schema/format generation pinned in rag_meta. Both the
	// read and the write path refuse an index at any other generation: a newer one
	// so an older binary never misreads a future layout, an older one because
	// nothing migrates it today and the layouts differ in ways that would otherwise
	// surface as a query-time "no such column". Bump it for any change to the stored
	// schema or to the text handed to the embedder, since neither is a function of
	// anything else rag_meta pins.
	formatVersion = 2

	// dbFileName is the SQLite index file inside the store directory.
	dbFileName = "knowledge.db"

	// lockFileName is the advisory write-lock file inside the store directory. It
	// serializes knowledge index across processes, which MaxOpenConns(1) alone
	// cannot do since WAL lets multiple processes open the file.
	lockFileName = "knowledge.lock"

	// topKCeiling is the hard upper bound on how many chunks a single search may
	// return, regardless of the configured or requested top_k, bounding injected
	// tokens and per-call cost.
	topKCeiling = 20

	// MaxTopK is topKCeiling for callers that have to explain the clamp to a reader.
	// Search applies it silently, so a surface offering a top_k asks for a number it
	// may not get, and one whose help quotes its own copy of the number goes stale
	// without a build failure.
	MaxTopK = topKCeiling

	// defaultTopK is the retrieval count used when the config leaves top_k unset.
	defaultTopK = 5

	// defaultMaxInjectedTokens caps the total retrieved text a single search feeds
	// the model when the config leaves max_injected_tokens unset.
	defaultMaxInjectedTokens = 6000

	// dbFileMode and dirFileMode keep the index and its directory private: the file
	// holds verbatim document text in cleartext, so only the owner may read it.
	dbFileMode  = 0o600
	dirFileMode = 0o700
)

// idleReaderTimeout bounds how long the read pool keeps an idle connection, so no
// pooled connection holds a WAL snapshot open long enough to block checkpointing
// and grow the -wal file unbounded across a long agent session.
const idleReaderTimeout = 30 * time.Second

// Sentinel errors let callers distinguish a soft empty state (no index yet) and
// the config/index mismatches that require a rebuild from a genuine failure.
var (
	// ErrIndexNotBuilt reports that no index file exists yet. It is a soft state,
	// not a failure: the agent read path returns it so a missing store never bricks
	// startup, and the CLI turns it into "run: fisk-ai knowledge index".
	ErrIndexNotBuilt = errors.New("knowledge index has not been built")

	// ErrMetaMismatch reports that the configured embedding identity (model,
	// prefixes, normalization) differs from what the index was built with. The fix
	// is always a reindex.
	ErrMetaMismatch = errors.New("knowledge index was built with a different embedding configuration")

	// ErrDimensionMismatch reports that the live model's embedding dimension differs
	// from the one pinned in the index. It surfaces before any table create or
	// embedding spend on the write path, and as a query-time refusal on the read
	// path.
	ErrDimensionMismatch = errors.New("embedding dimension does not match the index")

	// ErrModelMismatch reports that the embeddings server answered with a different
	// model than the one it was asked for. It is never degraded to lexical and never
	// written past: the vectors come from the wrong space, and the dimensions can
	// still agree, so this is the only check that catches the substitution.
	ErrModelMismatch = errors.New("embeddings server served a different model than the one configured")

	// ErrFormatTooNew reports an index written by a newer fisk-ai than this one.
	ErrFormatTooNew = errors.New("knowledge index format is newer than this build supports")

	// ErrFormatTooOld reports an index written by an older fisk-ai. It is the mirror
	// of ErrFormatTooNew and the fix is the opposite one: nothing migrates such an
	// index today, so it is discarded and rebuilt from the documents rather than
	// upgraded. Every open refuses it, and 'knowledge reset --force' is the one
	// command that can act on it, since an index nothing can open has no rows to
	// clear.
	ErrFormatTooOld = errors.New("knowledge index was built by an older fisk-ai and cannot be read by this build")

	// ErrLocked reports that another knowledge index writer holds the advisory lock.
	ErrLocked = errors.New("another knowledge index is already running")

	// ErrFTS5Missing reports that the SQLite build lacks FTS5, which the lexical
	// tier requires. It should never happen with modernc.org/sqlite (FTS5 is
	// compiled in) but is checked so a broken build fails clearly.
	ErrFTS5Missing = errors.New("this SQLite build was compiled without FTS5")
)

// Store wraps the single SQLite index file. A read-only store (opened by the
// agent and the inspection CLI commands) may have a nil db when no index file
// exists yet, in which case every read reports ErrIndexNotBuilt. A writer store
// (opened by knowledge index) holds the advisory write lock for its lifetime.
type Store struct {
	db  *sql.DB
	emb Embedder // nil for the lexical-only tier

	dbPath   string
	dir      string
	readOnly bool
	lock     *writeLock // non-nil only for a writer that took the advisory lock

	topK              int
	maxInjectedTokens int

	// citations renders the mapped citation of every result this store returns. It
	// is built in newStore, since a store keeps no *config.Config to build one from
	// later and a writer answers searches too.
	citations *CitationMapper
}

// resolveDir returns the store directory for cfg: the configured directory when
// set, else knowledge/<identity>, mirroring the memory feature's layout. A relative
// result is rebased under storeDir when the caller set one, so runs sharing a process
// place their index deterministically; an absolute configured directory is honored
// verbatim and ignores storeDir, and an empty storeDir keeps the process-working-
// directory behavior. The agent and the knowledge CLI must pass the same storeDir or
// they resolve to different directories (see rag.Open's soft not-built state).
func resolveDir(cfg *config.Config, storeDir string) string {
	dir := cfg.Harness.RAG.Directory
	if dir == "" {
		dir = filepath.Join("knowledge", cfg.Identity)
	}
	if storeDir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(storeDir, dir)
	}

	return dir
}

// StorePath returns the index file path for cfg without opening or creating
// anything, so a caller can name the file in a message about an index it could not
// open.
func StorePath(cfg *config.Config, storeDir string) string {
	return filepath.Join(resolveDir(cfg, storeDir), dbFileName)
}

// StoreExists reports whether an index file exists for cfg, without opening it or
// validating its contents. The rm and reset CLI commands use it to avoid creating
// an empty store when there is nothing to act on, and so they work even against an
// index whose pinned embedding identity no longer matches the config.
func StoreExists(cfg *config.Config, storeDir string) (bool, error) {
	path := StorePath(cfg, storeDir)
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat knowledge index %q: %w", path, err)
	}

	return true, nil
}

// Destroy removes the index file and its WAL sidecars, discarding an index rather
// than clearing it. It exists for the one state no Reset can reach: an index from
// another format generation, which every open refuses, so there is no handle to
// delete rows through. It takes the same advisory write lock as OpenWriter, so it
// cannot race a live indexer, and refuses a symlink at any of the three paths
// rather than deleting through it. It returns the path it removed, and a store that
// was already absent is not an error.
func Destroy(cfg *config.Config, storeDir string) (string, error) {
	dir := resolveDir(cfg, storeDir)

	lock, err := acquireWriteLock(filepath.Join(dir, lockFileName))
	if err != nil {
		return "", err
	}
	defer lock.release()

	dbPath := filepath.Join(dir, dbFileName)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix

		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("checking knowledge index %q: %w", path, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("knowledge index path %q is a symlink; refusing to follow it", path)
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("removing knowledge index %q: %w", path, err)
		}
	}

	return dbPath, nil
}

// resolvedTopK and resolvedMaxInjectedTokens apply the defaults for a config that
// leaves the values unset.
func resolvedTopK(cfg *config.RAGConfig) int {
	if cfg.TopK <= 0 {
		return defaultTopK
	}
	if cfg.TopK > topKCeiling {
		return topKCeiling
	}

	return cfg.TopK
}

func resolvedMaxInjectedTokens(cfg *config.RAGConfig) int {
	if cfg.MaxInjectedTokens <= 0 {
		return defaultMaxInjectedTokens
	}

	return cfg.MaxInjectedTokens
}

// newStore builds every part of a Store that comes from the config alone: the
// embedder, the resolved directory, the index file path, the retrieval limits and
// the citation renderer. Open and OpenWriter both build their store through it, so
// a value derived from the config cannot reach one path and be missing from the
// other, and a reader and a writer opened from one config always agree on it.
//
// The returned store has no db and no lock. Those belong to the constructors: Open
// attaches a read-only handle once it has seen the file exists, and OpenWriter
// attaches the write handle along with the advisory lock it took. buildEmbedder is
// the only call here that can fail, and it holds no directory, lock or file, so a
// caller that gets an error has nothing to release.
func newStore(cfg *config.Config, storeDir string, readOnly bool) (*Store, error) {
	emb, err := buildEmbedder(cfg)
	if err != nil {
		return nil, err
	}

	dir := resolveDir(cfg, storeDir)

	return &Store{
		emb:               emb,
		dbPath:            filepath.Join(dir, dbFileName),
		dir:               dir,
		readOnly:          readOnly,
		topK:              resolvedTopK(cfg.Harness.RAG),
		maxInjectedTokens: resolvedMaxInjectedTokens(cfg.Harness.RAG),
		citations:         NewCitationMapper(cfg.RAGCitationRules()),
	}, nil
}

// Open opens the index for reading, the path the agent and the inspection CLI
// commands use. It validates the config (a malformed embeddings block fails here,
// before the agent loop) and builds the embedder when the vector tier is on, but a
// missing index file is not an error: it returns a Store whose reads report
// ErrIndexNotBuilt, so a first run never fails to start. When the file exists it
// validates the pinned embedding identity against the configured embedder and
// refuses a stale or too-new index rather than returning garbage rankings.
func Open(cfg *config.Config, storeDir string) (*Store, error) {
	s, err := newStore(cfg, storeDir, true)
	if err != nil {
		return nil, err
	}

	// A read-only connection against a nonexistent file errors (mode=ro does not
	// create), so stat first: a missing file is the soft not-built state, returned
	// without opening. WAL is set by the writer before any reader attaches; the
	// reader never runs that pragma.
	if _, err := os.Stat(s.dbPath); errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat knowledge index %q: %w", s.dbPath, err)
	}

	db, err := openDB(s.dbPath, true)
	if err != nil {
		return nil, err
	}
	s.db = db

	if err := s.verifyFTS5(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	// A reader cannot repair an index from another generation, so it names the fix
	// and stops rather than querying columns that layout does not have. This runs
	// before the identity check because a rebuild resolves both.
	if err := s.refuseUnusableIndex(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	if err := s.validateReadMeta(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

// OpenWriter opens the index for writing, the path knowledge index uses. It takes
// the cross-process advisory lock (failing fast with ErrLocked if another writer
// holds it), creates the store directory and file with private permissions, sets
// WAL, ensures the base schema and triggers, and validates the config the same way
// Open does. The vector table and its dimension are created later, during ingest,
// once the live model's dimension is known (see index.go), so this never contacts
// the embeddings server. Close releases the lock. An index from another format
// generation is refused here, before the schema statements run.
func OpenWriter(cfg *config.Config, storeDir string) (*Store, error) {
	s, err := newStore(cfg, storeDir, false)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(s.dir, dirFileMode); err != nil {
		return nil, fmt.Errorf("creating knowledge directory %q: %w", s.dir, err)
	}

	lock, err := acquireWriteLock(filepath.Join(s.dir, lockFileName))
	if err != nil {
		return nil, err
	}
	s.lock = lock

	// Create the file 0600 up front so SQLite does not create it under the umask,
	// which could leave it world-readable and defeat the intended private mode.
	if err := ensureFileMode(s.dbPath); err != nil {
		lock.release()
		return nil, err
	}

	db, err := openDB(s.dbPath, false)
	if err != nil {
		lock.release()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s.db = db

	ctx := context.Background()
	if err := s.verifyFTS5(ctx); err != nil {
		s.Close()
		return nil, err
	}
	// Before ensureBaseSchema, which is the statement that would otherwise no-op
	// against an older layout and leave the store half-migrated.
	if err := s.refuseUnusableIndex(ctx); err != nil {
		s.Close()
		return nil, err
	}
	// In one transaction so a process killed partway through leaves either the schema
	// this build writes or no schema at all. A half-created schema opens no better than
	// a half-dropped one: rag_meta is created last, so a store with chunks and no
	// manifest is what an interrupted creation leaves behind.
	err = s.withTx(ctx, func(tx *sql.Tx) error { return ensureBaseSchema(ctx, tx) })
	if err != nil {
		s.Close()
		return nil, err
	}
	// WAL creates -wal/-shm honoring the umask; re-assert private modes on the file
	// and its sidecars, refusing a symlink planted at any of the three paths.
	if err := enforcePerms(s.dbPath); err != nil {
		s.Close()
		return nil, err
	}

	return s, nil
}

// openDB opens the SQLite file with the reader or writer DSN. The reader is
// mode=ro (driver-enforced) plus query_only as defense in depth and carries no
// journal_mode pragma, which a read-only connection cannot run; the writer sets
// WAL, which persists on the file for every later opener, and auto_vacuum=FULL so
// deletes (rm, reset, reindex) return freed pages to the OS at commit rather than
// leaving the file at its high-water mark. auto_vacuum is set before journal_mode
// and takes effect only on a freshly created, empty database, so the writer must
// create the file (see OpenWriter). Both set a busy timeout and enable foreign
// keys on every connection.
func openDB(path string, readOnly bool) (*sql.DB, error) {
	var dsn string
	if readOnly {
		dsn = fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=query_only(1)", path)
	} else {
		dsn = fmt.Sprintf("file:%s?_pragma=auto_vacuum(FULL)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening knowledge index %q: %w", path, err)
	}

	if readOnly {
		// A long-lived reader must not pin the WAL and block checkpointing: keep the
		// idle pool tiny and short-lived so no idle connection holds a snapshot open
		// across queries. Each query uses its own short read transaction.
		db.SetMaxIdleConns(1)
		db.SetConnMaxIdleTime(idleReaderTimeout)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening knowledge index %q: %w", path, err)
	}

	return db, nil
}

// verifyFTS5 confirms the SQLite build has FTS5 compiled in, which the lexical
// tier depends on. modernc.org/sqlite ships it by default, so this is a guard
// against a surprising build rather than an expected failure.
func (s *Store) verifyFTS5(ctx context.Context) error {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&enabled)
	if err != nil {
		return fmt.Errorf("checking FTS5 support: %w", err)
	}
	if enabled == 0 {
		return ErrFTS5Missing
	}

	return nil
}

// chunksColumns is the column set ensureBaseSchema creates for the chunks table,
// in declaration order. It has to be stated separately because CREATE TABLE IF NOT
// EXISTS silently no-ops against a chunks table from an older layout: the stored
// schema keeps its own columns and the first ingest dies with "table chunks has no
// column named ...". Keep it in step with the CREATE TABLE below.
var chunksColumns = []string{"id", "document_id", "heading_path", "ordinal", "body"}

// baseSchemaObjects is every object ensureBaseSchema creates unconditionally. The
// vector table and its trigger are absent deliberately: they exist only when the
// vector tier is on, so their absence is a configuration, not a defect.
//
// The gate checks for all of these, not just the chunks columns, because a schema
// can gain an object without any column changing. An index written before the
// unstemmed table existed has the right columns and the right pinned format and
// still cannot answer what this build asks of it, and reporting zero literal counts
// as though they were measured is worse than refusing.
var baseSchemaObjects = []string{
	"documents",
	"chunks",
	"chunks_fts",
	"chunks_fts_exact",
	"chunks_vocab",
	"rag_meta",
	"chunks_ai",
	"chunks_ad",
	"chunks_au",
}

// indexCheck is what the format gate learned about the open database. At most one
// of the two fields is set; both unset means the index is this build's own, or that
// there is no schema yet.
type indexCheck struct {
	// tooNew reports a format newer than this build. It is refused everywhere the
	// older one is, but the fix is the opposite: upgrade fisk-ai, since discarding
	// it would throw away an index a newer binary can still read.
	tooNew int
	// tooOld describes why the index predates this build, or is empty when it does
	// not, phrased to complete "cannot be read because ...".
	tooOld string
}

// checkIndexFormat classifies the open database against this build's format and
// schema. An empty database is never too old: it is what ensureBaseSchema is about
// to create, and ensureFileMode creates the file before openDB, so file existence
// is not evidence of an index.
//
// Two checks are needed rather than one. The format alone misses a store the
// shipped reset cleared, because that reset emptied rag_meta but kept the table
// shape, leaving no pinned format to compare, and an unpinned manifest is also what
// a fresh schema has. The column shape settles those cases.
func (s *Store) checkIndexFormat(ctx context.Context) (indexCheck, error) {
	cols, err := s.tableColumns(ctx, "chunks")
	if err != nil {
		return indexCheck{}, err
	}
	if len(cols) == 0 {
		return indexCheck{}, nil
	}

	// A chunks table implies a rag_meta table: ensureBaseSchema has always created
	// both, so readMeta cannot fail for a missing table here.
	m, err := s.readMeta(ctx)
	if err != nil {
		return indexCheck{}, err
	}

	switch {
	case m.FormatVersion > formatVersion:
		return indexCheck{tooNew: m.FormatVersion}, nil

	case m.FormatVersion > 0 && m.FormatVersion < formatVersion:
		return indexCheck{tooOld: fmt.Sprintf("its format_version is %d and this build writes %d", m.FormatVersion, formatVersion)}, nil

	case !slices.Equal(cols, chunksColumns):
		return indexCheck{tooOld: fmt.Sprintf("its chunks table has columns (%s) where this build needs (%s)",
			strings.Join(cols, ", "), strings.Join(chunksColumns, ", "))}, nil
	}

	missing, err := s.missingSchemaObjects(ctx)
	if err != nil {
		return indexCheck{}, err
	}
	if len(missing) > 0 {
		return indexCheck{tooOld: fmt.Sprintf("its schema is missing %s", strings.Join(missing, ", "))}, nil
	}

	return indexCheck{}, nil
}

// missingSchemaObjects returns the base schema objects absent from the open
// database, in declaration order. CREATE ... IF NOT EXISTS adds what is missing, so
// this is not about what the writer can repair; it is about a reader, and about a
// writer whose repair would leave the existing rows unindexed by the new table.
func (s *Store) missingSchemaObjects(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master`)
	if err != nil {
		return nil, fmt.Errorf("reading knowledge schema: %w", err)
	}
	defer rows.Close()

	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("reading knowledge schema: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var missing []string
	for _, name := range baseSchemaObjects {
		if !present[name] {
			missing = append(missing, name)
		}
	}

	return missing, nil
}

// formatTooNewError renders the refusal for an index from a newer build, whose fix
// is to run the binary that wrote it rather than to discard anything.
func formatTooNewError(pinned int) error {
	return fmt.Errorf("%w: index format_version=%d, this build supports up to %d; upgrade fisk-ai", ErrFormatTooNew, pinned, formatVersion)
}

// refuseUnusableIndex fails when the open database is from any format generation
// but this one. Every path refuses, readers and writers alike: there is no
// migration, so the index is discarded and rebuilt from the documents, and
// discarding it stays a thing the operator does deliberately rather than something
// an ordinary index run does on their behalf.
func (s *Store) refuseUnusableIndex(ctx context.Context) error {
	check, err := s.checkIndexFormat(ctx)
	if err != nil {
		return err
	}

	switch {
	case check.tooNew > 0:
		return formatTooNewError(check.tooNew)
	case check.tooOld != "":
		return fmt.Errorf("%w: the index at %q cannot be read because %s; discard it with 'fisk-ai knowledge reset --force' and rebuild it with 'fisk-ai knowledge index'", ErrFormatTooOld, s.dir, check.tooOld)
	}

	return nil
}

// schemaDropStatements removes every schema object, in the order it removes them.
// The triggers go first: DROP TABLE fires no triggers of its own, but the foreign
// key from chunks to documents does cascade, and a cascade into a live delete
// trigger is the path that fails SQLITE_CORRUPT against a broken index. With no
// triggers left, drop order stops mattering at all. chunks_vocab is dropped before
// the table it reads.
var schemaDropStatements = []string{
	`DROP TRIGGER IF EXISTS chunks_ai`,
	`DROP TRIGGER IF EXISTS chunks_ad`,
	`DROP TRIGGER IF EXISTS chunks_au`,
	`DROP TRIGGER IF EXISTS chunks_ad_vec`,
	`DROP TABLE IF EXISTS chunks_vocab`,
	`DROP TABLE IF EXISTS chunks_fts`,
	`DROP TABLE IF EXISTS chunks_fts_exact`,
	`DROP TABLE IF EXISTS chunks_vec`,
	`DROP TABLE IF EXISTS chunks`,
	`DROP TABLE IF EXISTS documents`,
	`DROP TABLE IF EXISTS rag_meta`,
}

// execer is the part of *sql.DB and *sql.Tx the schema statements need. The schema
// functions take one rather than reaching for s.db so a caller can run the whole
// sequence inside its transaction: the writer pool holds a single connection, so a
// function that opened its own transaction while the caller held one would wait for a
// connection that cannot be released until it returns.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// dropSchema removes every schema object, leaving an empty database for
// ensureBaseSchema to recreate.
//
// Dropping is what makes a reset a repair rather than another way to hit the same
// wall. Against an index that no longer matches its content table, clearing rows
// fails SQLITE_CORRUPT before any rebuild statement can run, because the cascade
// fires the delete trigger into the broken index; DROP TABLE fires no row triggers,
// so the corruption stops mattering instead of needing to be repaired. It also
// leaves ensureBaseSchema as the single definition of the schema, so a table added
// there needs no matching edit here beyond its own drop, where the alternative is a
// per-table rebuild list that has to be kept in step forever.
func dropSchema(ctx context.Context, ex execer) error {
	for _, stmt := range schemaDropStatements {
		if _, err := ex.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("dropping knowledge schema: %w", err)
		}
	}

	return nil
}

// tableColumns returns the column names of table in declaration order, or an empty
// slice when the table does not exist.
func (s *Store) tableColumns(ctx context.Context, table string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("reading knowledge schema: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("reading knowledge schema: %w", err)
		}
		out = append(out, name)
	}

	return out, rows.Err()
}

// ensureBaseSchema creates the documents and chunks tables, the external-content
// FTS5 index, the FTS sync triggers, and the rag_meta manifest. The triggers are
// the sole path that keeps chunks_fts in step with chunks; application code never
// writes chunks_fts directly. The vector table and its delete trigger are created
// later, during ingest, once the dimension is known.
//
// chunks.body is the chunk body alone. The heading breadcrumb lives in
// heading_path and nowhere else, so body: and heading: are answerable separately
// and a phrase cannot match across the join between them. Only the embedder sees
// the two folded together, at its one call site in index.go.
//
// There are two FTS tables over the same rows because FTS5 sets the tokenizer per
// table rather than per column. chunks_fts stems, and is what every query runs
// against, so a zero result means no document holds the word in any form. Its
// prefix behavior is not monotonic, though, and its vocabulary is stems rather than
// words, which is what chunks_fts_exact is for: it earns its size by naming the
// real words behind a stem, by being the only table where a prefix search grows
// monotonically, and by letting a stemmed count state how many documents contain
// the word as it was typed. chunks_vocab exposes that table's terms with their
// frequencies, and is writer-created because a read-only connection cannot create
// a virtual table at all.
//
// Three rules govern the triggers, each of which corrupts silently when broken.
// The hidden first column of a command insert is the target table's own name, so a
// copy-paste that leaves the wrong name there writes into the wrong index. Every
// indexed column must be supplied on a delete, with the old value, or terms are
// left behind against a rowid that no longer exists and every later delete wedges
// SQLITE_CORRUPT. And the FTS5 column names must equal the content-table column
// names: a mismatch still answers MATCH and still passes a bare integrity check,
// failing only at rebuild, which is why the body rename lands in both DDLs at once.
func ensureBaseSchema(ctx context.Context, ex execer) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id    INTEGER PRIMARY KEY,
			path  TEXT NOT NULL UNIQUE,
			title TEXT,
			mtime INTEGER,
			hash  TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id           INTEGER PRIMARY KEY,
			document_id  INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			heading_path TEXT,
			ordinal      INTEGER,
			body         TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
			body,
			heading_path,
			content='chunks',
			content_rowid='id',
			tokenize='porter unicode61'
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts_exact USING fts5(
			body,
			heading_path,
			content='chunks',
			content_rowid='id',
			tokenize='unicode61'
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vocab USING fts5vocab('chunks_fts_exact', 'row')`,
		`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
			INSERT INTO chunks_fts(rowid, body, heading_path)
			VALUES (new.id, new.body, new.heading_path);
			INSERT INTO chunks_fts_exact(rowid, body, heading_path)
			VALUES (new.id, new.body, new.heading_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
			INSERT INTO chunks_fts(chunks_fts, rowid, body, heading_path)
			VALUES ('delete', old.id, old.body, old.heading_path);
			INSERT INTO chunks_fts_exact(chunks_fts_exact, rowid, body, heading_path)
			VALUES ('delete', old.id, old.body, old.heading_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
			INSERT INTO chunks_fts(chunks_fts, rowid, body, heading_path)
			VALUES ('delete', old.id, old.body, old.heading_path);
			INSERT INTO chunks_fts_exact(chunks_fts_exact, rowid, body, heading_path)
			VALUES ('delete', old.id, old.body, old.heading_path);
			INSERT INTO chunks_fts(rowid, body, heading_path)
			VALUES (new.id, new.body, new.heading_path);
			INSERT INTO chunks_fts_exact(rowid, body, heading_path)
			VALUES (new.id, new.body, new.heading_path);
		END`,
		`CREATE TABLE IF NOT EXISTS rag_meta (key TEXT PRIMARY KEY, value TEXT)`,
	}

	for _, stmt := range stmts {
		if _, err := ex.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating knowledge schema: %w", err)
		}
	}

	return nil
}

// Close releases the DB handle and, for a writer, the advisory lock. Both are
// attempted so a lock is never leaked even if the DB close errors.
func (s *Store) Close() error {
	var errs []error
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
		s.db = nil
	}
	if s.lock != nil {
		if err := s.lock.release(); err != nil {
			errs = append(errs, err)
		}
		s.lock = nil
	}

	return errors.Join(errs...)
}

// Built reports whether an index file was present when the store was opened. A
// read-only store over a missing file has no db and reports false.
func (s *Store) Built() bool { return s.db != nil }

// VectorEnabled reports whether the vector tier is active for this store: an
// embedder is configured.
func (s *Store) VectorEnabled() bool { return s.emb != nil }

// Path returns the index file path, for status output and error messages.
func (s *Store) Path() string { return s.dbPath }

// MaxInjectedTokens is the cap on the total retrieved text a single search feeds
// the model, resolved from the config with its default applied.
func (s *Store) MaxInjectedTokens() int { return s.maxInjectedTokens }

// Dir returns the store directory, excluded from its own index walk.
func (s *Store) Dir() string { return s.dir }

// CitationMapper returns the renderer this store built from the configured
// citation rules. Every Hit and MatchedDoc already carries its mapped citation, so
// this is for a surface that cites a document without running a search, such as a
// listing of the indexed documents.
func (s *Store) CitationMapper() *CitationMapper { return s.citations }

// Meta is the pinned vector identity read from rag_meta. FormatVersion is always
// present; the remaining fields are set only for an index built with the vector
// tier.
type Meta struct {
	FormatVersion  int
	Model          string
	Dimension      int
	Normalized     bool
	QueryPrefix    string
	DocumentPrefix string
}

// readMeta loads the rag_meta manifest. A store with no rag_meta rows (a
// freshly-created base schema) returns a zero Meta with FormatVersion unset, which
// the callers treat as "not yet pinned".
func (s *Store) readMeta(ctx context.Context) (Meta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM rag_meta`)
	if err != nil {
		return Meta{}, fmt.Errorf("reading knowledge manifest: %w", err)
	}
	defer rows.Close()

	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Meta{}, fmt.Errorf("reading knowledge manifest: %w", err)
		}
		kv[k] = v
	}
	if err := rows.Err(); err != nil {
		return Meta{}, fmt.Errorf("reading knowledge manifest: %w", err)
	}

	var m Meta
	if v, ok := kv["format_version"]; ok {
		m.FormatVersion, _ = strconv.Atoi(v)
	}
	m.Model = kv["model"]
	if v, ok := kv["dimension"]; ok {
		m.Dimension, _ = strconv.Atoi(v)
	}
	m.Normalized = kv["normalized"] == "true"
	m.QueryPrefix = kv["query_prefix"]
	m.DocumentPrefix = kv["document_prefix"]

	return m, nil
}

// writeMeta pins the manifest inside tx, replacing any existing values. It is
// called from ingest when the vector table is (re)created, so the pinned identity
// always matches the vectors on disk.
func writeMeta(ctx context.Context, tx *sql.Tx, m Meta) error {
	pairs := [][2]string{
		{"format_version", strconv.Itoa(m.FormatVersion)},
		{"model", m.Model},
		{"dimension", strconv.Itoa(m.Dimension)},
		{"normalized", boolText(m.Normalized)},
		{"query_prefix", m.QueryPrefix},
		{"document_prefix", m.DocumentPrefix},
	}
	for _, p := range pairs {
		_, err := tx.ExecContext(ctx, `INSERT INTO rag_meta(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, p[0], p[1])
		if err != nil {
			return fmt.Errorf("writing knowledge manifest: %w", err)
		}
	}

	return nil
}

// validateReadMeta checks the pinned manifest against this store's configured
// embedder before any query runs: when the vector tier is configured it refuses an
// index whose pinned model, prefixes, or normalization differ from the
// configuration (a stale index that would return garbage rankings) or that was
// built lexical-only. Dimension is validated at query time against the live model's
// probe so Open never contacts the server. The format generation itself is settled
// before this runs, by refuseUnusableIndex, which is the single place both
// directions are refused.
func (s *Store) validateReadMeta(ctx context.Context) error {
	m, err := s.readMeta(ctx)
	if err != nil {
		return err
	}

	if s.emb == nil {
		return nil
	}

	// The vector tier is configured. An index built lexical-only has no pinned
	// model; refuse and point at a reindex rather than silently searching lexical
	// forever when the operator asked for hybrid.
	if m.Model == "" {
		return fmt.Errorf("%w: config requests embeddings model=%q but the index was built lexical-only; run 'fisk-ai knowledge index --reindex'", ErrMetaMismatch, s.emb.Model())
	}

	if m.Model != s.emb.Model() || m.QueryPrefix != s.emb.QueryPrefix() || m.DocumentPrefix != s.emb.DocumentPrefix() || !m.Normalized {
		return fmt.Errorf("%w: index built with model=%q query_prefix=%q document_prefix=%q normalized=%v; config requests model=%q query_prefix=%q document_prefix=%q; run 'fisk-ai knowledge index --reindex'",
			ErrMetaMismatch, m.Model, m.QueryPrefix, m.DocumentPrefix, m.Normalized,
			s.emb.Model(), s.emb.QueryPrefix(), s.emb.DocumentPrefix())
	}

	return nil
}

// ensureFileMode creates the DB file with the private mode if it does not exist,
// so SQLite does not create it under the umask. An existing file is left as is
// (enforcePerms re-asserts its mode after WAL setup).
func ensureFileMode(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|openNoFollow, dbFileMode)
	if err != nil {
		return fmt.Errorf("creating knowledge index %q: %w", path, err)
	}

	return f.Close()
}

// enforcePerms re-asserts the private mode on the DB file and its -wal/-shm
// sidecars after WAL setup, since SQLite creates the sidecars honoring the umask
// and they can otherwise land world-readable. It refuses a symlink planted at any
// of the three paths, so a reader cannot be redirected to an unrelated file.
func enforcePerms(dbPath string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix
		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking knowledge index perms %q: %w", path, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("knowledge index path %q is a symlink; refusing to follow it", path)
		}
		if err := os.Chmod(path, dbFileMode); err != nil {
			return fmt.Errorf("setting knowledge index perms %q: %w", path, err)
		}
	}

	return nil
}

// boolText renders a bool as the manifest's canonical "true"/"false" text.
func boolText(b bool) string {
	if b {
		return "true"
	}

	return "false"
}
