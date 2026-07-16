//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// maxIndexFileBytes skips a source file larger than this, bounding memory and
	// keeping a single chunk within the embedding input cap.
	maxIndexFileBytes = 512 * 1024

	// memoryDirName is the sibling store excluded from every walk, alongside this
	// feature's own store directory.
	memoryDirName = "memory"
)

// DefaultExtensions is the set of file extensions the walk indexes: markdown and
// plain text only.
var DefaultExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
	".text":     true,
}

// IndexOptions controls one index run.
type IndexOptions struct {
	// Reindex forces a full rebuild: existing data and the vector table are dropped
	// (allowing a dimension change) and everything is re-embedded from scratch.
	Reindex bool
	// DryRun lists what would be done and estimates chunk and embedding-call counts
	// without writing or embedding anything.
	DryRun bool
	// Reconcile enables orphan deletion after the walk. It is set only for a
	// full-corpus walk (no explicit path given); a subpath walk never deletes.
	Reconcile bool
	// Extensions is the allowed extension set; DefaultExtensions when nil.
	Extensions map[string]bool
	// Progress, when set, receives human-readable progress notes (skipped files,
	// counts) for the CLI to print. It is never called with model-facing data.
	Progress func(string)
}

// IndexStats summarizes an index run.
type IndexStats struct {
	Files      int
	Added      int
	Updated    int
	Skipped    int
	Removed    int
	Chunks     int
	Embeddings int
	// FirstBuild reports that the index held no documents before this run, the one
	// time the operator has no cost intuition for what is about to be embedded.
	FirstBuild bool
}

func (o IndexOptions) exts() map[string]bool {
	if o.Extensions != nil {
		return o.Extensions
	}

	return DefaultExtensions
}

func (o IndexOptions) note(msg string) {
	if o.Progress != nil {
		o.Progress(msg)
	}
}

// Index walks roots and brings the index into line with them: it adds new files,
// re-ingests changed ones (by content hash), skips unchanged ones, and, on a
// reconciling full-corpus walk, deletes documents no longer present. The vector
// tier is prepared upfront (dimension probed and pinned, a mismatch refused before
// any embedding spend). Embedding happens outside the write transaction so the
// slow call never holds the single writer slot. It requires a writer store.
func (s *Store) Index(ctx context.Context, roots []string, opts IndexOptions) (*IndexStats, error) {
	if s.readOnly || s.db == nil {
		return nil, fmt.Errorf("index requires a writable knowledge store")
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("index requires at least one path")
	}

	stats := &IndexStats{}

	priorDocs, err := scanCount(ctx, s.db, `SELECT count(*) FROM documents`)
	if err != nil {
		return nil, err
	}
	stats.FirstBuild = priorDocs == 0

	if opts.Reindex && !opts.DryRun {
		if err := s.resetForReindex(ctx); err != nil {
			return nil, err
		}
		stats.FirstBuild = true
	}

	if !opts.DryRun {
		if err := s.prepareVectorTier(ctx, opts.Reindex); err != nil {
			return nil, err
		}
	}

	seen := map[string]bool{}
	for _, root := range roots {
		if err := s.walkRoot(ctx, root, opts, stats, seen); err != nil {
			return nil, err
		}
	}

	// Orphan reconcile: only on a reconciling full-corpus walk, and never when the
	// walk saw zero files (a walk that errored early must not wipe the index).
	if opts.Reconcile && !opts.DryRun && len(seen) > 0 {
		if err := s.reconcileOrphans(ctx, seen, stats); err != nil {
			return nil, err
		}
	}

	return stats, nil
}

// walkRoot walks one root, dispatching each eligible file to add / update / skip.
func (s *Store) walkRoot(ctx context.Context, root string, opts IndexOptions, stats *IndexStats, seen map[string]bool) error {
	exts := opts.exts()
	storeDir := filepath.Clean(s.dir)

	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return s.skipDir(path, d, storeDir)
		}

		// Skip symlinks: canonicalize to one real key per file and never follow a
		// link out of the corpus.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !exts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxIndexFileBytes {
			opts.note(fmt.Sprintf("skipping oversized file %q (%d bytes)", path, info.Size()))
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			opts.note(fmt.Sprintf("skipping non-UTF-8 file %q", path))
			return nil
		}

		key := filepath.ToSlash(path)
		seen[key] = true
		stats.Files++

		return s.ingestOne(ctx, key, info.ModTime().Unix(), data, opts, stats)
	})
}

// skipDir decides whether to descend into a directory. It skips dotfiles, the
// feature's own store directory, and a sibling memory/ store, so the index never
// walks its own file or the memory feature's.
func (s *Store) skipDir(path string, d os.DirEntry, storeDir string) error {
	name := d.Name()
	if filepath.Clean(path) == storeDir {
		return filepath.SkipDir
	}
	// Skip dotfiles like .git, but never skip the walk root itself when it is ".".
	if name != "." && name != ".." && strings.HasPrefix(name, ".") {
		return filepath.SkipDir
	}
	if name == memoryDirName {
		return filepath.SkipDir
	}

	return nil
}

// ingestOne classifies a seen file by content hash and add/update/skips it,
// updating stats. In dry-run it only chunks to estimate work and writes nothing.
func (s *Store) ingestOne(ctx context.Context, key string, mtime int64, data []byte, opts IndexOptions, stats *IndexStats) error {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	var have string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM documents WHERE path = ?`, key).Scan(&have)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// add
	case err != nil:
		return err
	case have == hash:
		stats.Skipped++
		n, err := scanCount(ctx, s.db, `SELECT count(*) FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.path=?`, key)
		if err != nil {
			return err
		}
		stats.Chunks += n
		return nil
	default:
		// update
	}

	chunks := ChunkDocument(string(data))

	if opts.DryRun {
		if errors.Is(err, sql.ErrNoRows) {
			stats.Added++
		} else {
			stats.Updated++
		}
		stats.Chunks += len(chunks)
		if s.emb != nil {
			stats.Embeddings += len(chunks)
		}
		return nil
	}

	if err := s.ingestFile(ctx, key, mtime, hash, string(data), chunks); err != nil {
		return fmt.Errorf("indexing %q: %w", key, err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		stats.Added++
	} else {
		stats.Updated++
	}
	stats.Chunks += len(chunks)
	if s.emb != nil {
		stats.Embeddings += len(chunks)
	}

	return nil
}

// ingestFile embeds the file's chunks OUTSIDE the write transaction (the slow call
// must not hold the single writer slot), then does the cheap upsert + purge +
// insert in a short transaction. Purge-then-insert covers both first ingest and
// update; the triggers keep chunks_fts and chunks_vec correct, including clearing
// ghost chunks when a file shrinks.
func (s *Store) ingestFile(ctx context.Context, key string, mtime int64, hash, contents string, chunks []Chunk) error {
	var vecs [][]float32
	if s.emb != nil && len(chunks) > 0 {
		docs := make([]Document, len(chunks))
		for i, c := range chunks {
			docs[i] = Document{Title: c.HeadingPath, Text: c.Content}
		}
		raw, err := s.emb.EmbedDocuments(ctx, docs)
		if err != nil {
			return fmt.Errorf("embedding: %w", err)
		}
		if len(raw) != len(chunks) {
			return fmt.Errorf("embedder returned %d vectors for %d chunks", len(raw), len(chunks))
		}
		vecs = make([][]float32, len(raw))
		for i, v := range raw {
			vecs[i] = normalize(v)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var docID int64
	title := DocumentTitle(contents)
	err = tx.QueryRowContext(ctx,
		`INSERT INTO documents(path, title, mtime, hash) VALUES(?,?,?,?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, mtime=excluded.mtime, hash=excluded.hash
		 RETURNING id`, key, title, mtime, hash).Scan(&docID)
	if err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}

	// Purge the document's existing chunks first; the triggers clear the matching
	// FTS and vec rows, so a shrinking file leaves no ghost chunks behind.
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("purge chunks: %w", err)
	}

	for i, c := range chunks {
		var chunkID int64
		err = tx.QueryRowContext(ctx,
			`INSERT INTO chunks(document_id, heading_path, ordinal, content) VALUES(?,?,?,?) RETURNING id`,
			docID, c.HeadingPath, i, c.Content).Scan(&chunkID)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
		if s.emb != nil {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO chunks_vec(chunk_id, embedding) VALUES(?, ?)`, chunkID, vecJSON(vecs[i])); err != nil {
				return fmt.Errorf("insert vector: %w", err)
			}
		}
	}

	return tx.Commit()
}

// reconcileOrphans deletes documents whose path was not seen during a reconciling
// full-corpus walk, so files removed from disk drop out of the index.
func (s *Store) reconcileOrphans(ctx context.Context, seen map[string]bool, stats *IndexStats) error {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM documents`)
	if err != nil {
		return err
	}
	var orphans []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if !seen[p] {
			orphans = append(orphans, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range orphans {
		if _, err := s.DeleteDocument(ctx, p); err != nil {
			return err
		}
		stats.Removed++
	}

	return nil
}

// DeleteDocument removes one document and its chunks by path; the triggers clear
// the FTS and vec rows. It is idempotent: an absent path is not an error. It
// reports whether a matching document was found and removed.
func (s *Store) DeleteDocument(ctx context.Context, key string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var docID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM documents WHERE path = ?`, key).Scan(&docID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, docID); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	return true, nil
}

// prepareVectorTier probes the live model's dimension and reconciles it with the
// manifest before any embedding spend (invariant 1). It creates the vector table
// and pins the manifest on a fresh or reindexed store, and refuses upfront when the
// configured embedding identity differs from an existing index, naming the fix. On
// a lexical-only store it pins just the format version. It contacts the embeddings
// server (the dimension probe), so a configured-but-unreachable server fails loud
// here, at index time.
func (s *Store) prepareVectorTier(ctx context.Context, reindex bool) error {
	if s.emb == nil {
		return s.pinLexicalMeta(ctx)
	}

	dim, err := s.emb.Dim(ctx)
	if err != nil {
		return fmt.Errorf("probing embedding dimension: %w", err)
	}

	desired := Meta{
		FormatVersion:  formatVersion,
		Model:          s.emb.Model(),
		Dimension:      dim,
		Normalized:     true,
		QueryPrefix:    s.emb.QueryPrefix(),
		DocumentPrefix: s.emb.DocumentPrefix(),
	}

	meta, err := s.readMeta(ctx)
	if err != nil {
		return err
	}

	if !reindex && meta.Model != "" {
		if meta.Model != desired.Model || meta.Dimension != desired.Dimension ||
			meta.QueryPrefix != desired.QueryPrefix || meta.DocumentPrefix != desired.DocumentPrefix || !meta.Normalized {
			return fmt.Errorf("%w: manifest built with model=%s dim=%d; config requests model=%s dim=%d - run 'fisk-ai knowledge index --reindex'",
				ErrMetaMismatch, meta.Model, meta.Dimension, desired.Model, desired.Dimension)
		}
	}

	if !reindex && meta.Model == "" {
		chunkCount, err := scanCount(ctx, s.db, `SELECT count(*) FROM chunks`)
		if err != nil {
			return err
		}
		if chunkCount > 0 {
			return fmt.Errorf("%w: the existing index is lexical-only but config now requests embeddings model=%s - run 'fisk-ai knowledge index --reindex'", ErrMetaMismatch, desired.Model)
		}
	}

	return s.createVectorObjects(ctx, dim, desired)
}

// createVectorObjects creates the vec0 table (at the given, already-reconciled
// dimension) and its delete trigger if absent, then pins the manifest. A reindex
// drops the table first, so IF NOT EXISTS here is safe and never a silent
// dimension no-op.
func (s *Store) createVectorObjects(ctx context.Context, dim int, meta Meta) error {
	stmts := []string{
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(
			chunk_id  INTEGER PRIMARY KEY,
			embedding FLOAT[%d]
		)`, dim),
		`CREATE TRIGGER IF NOT EXISTS chunks_ad_vec AFTER DELETE ON chunks BEGIN
			DELETE FROM chunks_vec WHERE chunk_id = old.id;
		END`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating vector table (dimension %d): %w", dim, err)
		}
	}

	return s.withTx(ctx, func(tx *sql.Tx) error { return writeMeta(ctx, tx, meta) })
}

// pinLexicalMeta records just the format version for a lexical-only index, so the
// read path can validate the format and a later switch to the vector tier detects
// the lexical-only build (empty model) and requires a reindex.
func (s *Store) pinLexicalMeta(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return writeMeta(ctx, tx, Meta{FormatVersion: formatVersion})
	})
}

// Reset wipes all indexed data from an open writer store, leaving a clean empty
// index: the file and base schema remain, ready for the next knowledge index. It
// drops the vector table (so a later model or dimension change is unconstrained),
// clears the manifest, and runs the FTS 'rebuild' repair. It works even against an
// index whose pinned embedding identity no longer matches the config.
func (s *Store) Reset(ctx context.Context) error {
	return s.resetForReindex(ctx)
}

// resetForReindex drops the vector objects (so the dimension can change), clears
// all data (the FK cascade plus triggers clear chunks and the FTS/vec rows),
// clears the manifest, and runs the FTS 'rebuild' repair, leaving a clean empty
// index for a full rebuild.
func (s *Store) resetForReindex(ctx context.Context) error {
	stmts := []string{
		`DROP TRIGGER IF EXISTS chunks_ad_vec`,
		`DROP TABLE IF EXISTS chunks_vec`,
		`DELETE FROM documents`,
		`DELETE FROM rag_meta`,
		`INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("resetting index for reindex: %w", err)
		}
	}

	return nil
}

// withTx runs fn in a transaction, committing on success and rolling back on error.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}
