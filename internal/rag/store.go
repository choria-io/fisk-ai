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
	"strconv"
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
	// formatVersion is the schema/format generation pinned in rag_meta. The read
	// path refuses an index whose format_version is newer than this, so an older
	// binary never misreads a future layout.
	formatVersion = 1

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

	// ErrFormatTooNew reports an index written by a newer fisk-ai than this one.
	ErrFormatTooNew = errors.New("knowledge index format is newer than this build supports")

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

// StoreExists reports whether an index file exists for cfg, without opening it or
// validating its contents. The rm and reset CLI commands use it to avoid creating
// an empty store when there is nothing to act on, and so they work even against an
// index whose pinned embedding identity no longer matches the config.
func StoreExists(cfg *config.Config, storeDir string) (bool, error) {
	path := filepath.Join(resolveDir(cfg, storeDir), dbFileName)
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat knowledge index %q: %w", path, err)
	}

	return true, nil
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

// Open opens the index for reading, the path the agent and the inspection CLI
// commands use. It validates the config (a malformed embeddings block fails here,
// before the agent loop) and builds the embedder when the vector tier is on, but a
// missing index file is not an error: it returns a Store whose reads report
// ErrIndexNotBuilt, so a first run never fails to start. When the file exists it
// validates the pinned embedding identity against the configured embedder and
// refuses a stale or too-new index rather than returning garbage rankings.
func Open(cfg *config.Config, storeDir string) (*Store, error) {
	emb, err := buildEmbedder(cfg)
	if err != nil {
		return nil, err
	}

	dir := resolveDir(cfg, storeDir)
	dbPath := filepath.Join(dir, dbFileName)

	s := &Store{
		emb:               emb,
		dbPath:            dbPath,
		dir:               dir,
		readOnly:          true,
		topK:              resolvedTopK(cfg.Harness.RAG),
		maxInjectedTokens: resolvedMaxInjectedTokens(cfg.Harness.RAG),
	}

	// A read-only connection against a nonexistent file errors (mode=ro does not
	// create), so stat first: a missing file is the soft not-built state, returned
	// without opening. WAL is set by the writer before any reader attaches; the
	// reader never runs that pragma.
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat knowledge index %q: %w", dbPath, err)
	}

	db, err := openDB(dbPath, true)
	if err != nil {
		return nil, err
	}
	s.db = db

	if err := s.verifyFTS5(context.Background()); err != nil {
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
// the embeddings server. Close releases the lock.
func OpenWriter(cfg *config.Config, storeDir string) (*Store, error) {
	emb, err := buildEmbedder(cfg)
	if err != nil {
		return nil, err
	}

	dir := resolveDir(cfg, storeDir)
	if err := os.MkdirAll(dir, dirFileMode); err != nil {
		return nil, fmt.Errorf("creating knowledge directory %q: %w", dir, err)
	}

	lock, err := acquireWriteLock(filepath.Join(dir, lockFileName))
	if err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dir, dbFileName)

	// Create the file 0600 up front so SQLite does not create it under the umask,
	// which could leave it world-readable and defeat the intended private mode.
	if err := ensureFileMode(dbPath); err != nil {
		lock.release()
		return nil, err
	}

	db, err := openDB(dbPath, false)
	if err != nil {
		lock.release()
		return nil, err
	}
	db.SetMaxOpenConns(1)

	s := &Store{
		db:                db,
		emb:               emb,
		dbPath:            dbPath,
		dir:               dir,
		readOnly:          false,
		lock:              lock,
		topK:              resolvedTopK(cfg.Harness.RAG),
		maxInjectedTokens: resolvedMaxInjectedTokens(cfg.Harness.RAG),
	}

	ctx := context.Background()
	if err := s.verifyFTS5(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.ensureBaseSchema(ctx); err != nil {
		s.Close()
		return nil, err
	}
	// WAL creates -wal/-shm honoring the umask; re-assert private modes on the file
	// and its sidecars, refusing a symlink planted at any of the three paths.
	if err := enforcePerms(dbPath); err != nil {
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

// ensureBaseSchema creates the documents and chunks tables, the external-content
// FTS5 index, the FTS sync triggers, and the rag_meta manifest. The triggers are
// the sole path that keeps chunks_fts in step with chunks; application code never
// writes chunks_fts directly. The vector table and its delete trigger are created
// later, during ingest, once the dimension is known.
func (s *Store) ensureBaseSchema(ctx context.Context) error {
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
			content      TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
			content,
			heading_path,
			content='chunks',
			content_rowid='id',
			tokenize='porter unicode61'
		)`,
		`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
			INSERT INTO chunks_fts(rowid, content, heading_path)
			VALUES (new.id, new.content, new.heading_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
			INSERT INTO chunks_fts(chunks_fts, rowid, content, heading_path)
			VALUES ('delete', old.id, old.content, old.heading_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
			INSERT INTO chunks_fts(chunks_fts, rowid, content, heading_path)
			VALUES ('delete', old.id, old.content, old.heading_path);
			INSERT INTO chunks_fts(rowid, content, heading_path)
			VALUES (new.id, new.content, new.heading_path);
		END`,
		`CREATE TABLE IF NOT EXISTS rag_meta (key TEXT PRIMARY KEY, value TEXT)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
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
// embedder before any query runs. It refuses a too-new format outright, and, when
// the vector tier is configured, refuses an index whose pinned model, prefixes, or
// normalization differ from the configuration (a stale index that would return
// garbage rankings) or that was built lexical-only. Dimension is validated at
// query time against the live model's probe so Open never contacts the server.
func (s *Store) validateReadMeta(ctx context.Context) error {
	m, err := s.readMeta(ctx)
	if err != nil {
		return err
	}

	// A base schema with no pinned format yet (empty index) is acceptable; a search
	// reports index_empty. Only a present, newer format is refused.
	if m.FormatVersion > formatVersion {
		return fmt.Errorf("%w: index format_version=%d, this build supports up to %d; upgrade fisk-ai", ErrFormatTooNew, m.FormatVersion, formatVersion)
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
