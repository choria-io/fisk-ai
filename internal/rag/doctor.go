//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// Stats is the full picture of an index, rendered by knowledge stats: the doc,
// chunk and vector counts, the pinned vector identity, and the on-disk footprint.
type Stats struct {
	Built        bool
	Documents    int
	Chunks       int
	Vectors      int
	Meta         Meta
	StorePath    string
	DBSize       int64
	WALSize      int64
	LastModified time.Time
	VectorTier   bool
}

// Stats gathers the index counts and on-disk sizes. A store with no index file
// reports Built false and zero counts.
func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{StorePath: s.dbPath, VectorTier: s.emb != nil, Built: s.db != nil}
	if s.db == nil {
		return st, nil
	}

	var err error
	if st.Documents, err = scanCount(ctx, s.db, `SELECT count(*) FROM documents`); err != nil {
		return nil, err
	}
	if st.Chunks, err = scanCount(ctx, s.db, `SELECT count(*) FROM chunks`); err != nil {
		return nil, err
	}
	if st.Vectors, err = s.vectorCount(ctx); err != nil {
		return nil, err
	}
	if st.Meta, err = s.readMeta(ctx); err != nil {
		return nil, err
	}

	if fi, err := os.Stat(s.dbPath); err == nil {
		st.DBSize = fi.Size()
		st.LastModified = fi.ModTime()
	}
	if fi, err := os.Stat(s.dbPath + "-wal"); err == nil {
		st.WALSize = fi.Size()
	}

	return st, nil
}

// vectorCount returns the number of stored vectors, or 0 when the vector table
// does not exist (a lexical-only index).
func (s *Store) vectorCount(ctx context.Context) (int, error) {
	exists, err := s.tableExists(ctx, "chunks_vec")
	if err != nil || !exists {
		return 0, err
	}

	return scanCount(ctx, s.db, `SELECT count(*) FROM chunks_vec`)
}

// tableExists reports whether a table or virtual table of the given name exists.
func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// Source is one indexed file with its chunk count and last-modified time, listed
// by knowledge sources so the operator can discover what to feed knowledge show.
type Source struct {
	Path   string
	Chunks int
	MTime  time.Time
}

// Sources lists every indexed document with its chunk count, sorted by path.
func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	if s.db == nil {
		return nil, ErrIndexNotBuilt
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT d.path, COUNT(c.id), d.mtime
		 FROM documents d LEFT JOIN chunks c ON c.document_id = d.id
		 GROUP BY d.id ORDER BY d.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Source
	for rows.Next() {
		var src Source
		var mtime int64
		if err := rows.Scan(&src.Path, &src.Chunks, &mtime); err != nil {
			return nil, err
		}
		src.MTime = time.Unix(mtime, 0)
		out = append(out, src)
	}

	return out, rows.Err()
}

// ChunkText resolves a citation (<relpath>#<ordinal>) to one chunk's heading path
// and verbatim content, backing knowledge show. It reports ErrIndexNotBuilt when
// no index exists and sql.ErrNoRows when the citation resolves to nothing.
func (s *Store) ChunkText(ctx context.Context, relPath string, ordinal int) (headingPath, content string, err error) {
	if s.db == nil {
		return "", "", ErrIndexNotBuilt
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT c.heading_path, c.content
		 FROM chunks c JOIN documents d ON d.id = c.document_id
		 WHERE d.path = ? AND c.ordinal = ?`, relPath, ordinal).Scan(&headingPath, &content)
	if err != nil {
		return "", "", err
	}

	return headingPath, content, nil
}

// DoctorCheck is one preflight result. Fatal marks a check whose failure should
// make knowledge doctor exit non-zero; an absent embeddings server is never fatal,
// so a lexical-only user is not told their setup is broken.
type DoctorCheck struct {
	Name   string
	OK     bool
	Detail string
	Fatal  bool
}

// DoctorReport is the full doctor output: the canonical tier line and the ordered
// checks.
type DoctorReport struct {
	TierLine string
	Checks   []DoctorCheck
}

// HasFatal reports whether any fatal check failed, so the CLI can set the exit code.
func (r *DoctorReport) HasFatal() bool {
	for _, c := range r.Checks {
		if c.Fatal && !c.OK {
			return true
		}
	}

	return false
}

// Doctor runs the preflight checks: it always verifies the store, FTS5, WAL, write
// permission, and that the configured index paths resolve; only when the vector
// tier is configured does it probe the embeddings endpoint and check the manifest.
// It degrades for lexical users and never fails solely because embeddings are
// absent.
func (s *Store) Doctor(ctx context.Context, paths []string) (*DoctorReport, error) {
	tier, err := s.TierLine(ctx)
	if err != nil {
		return nil, err
	}
	report := &DoctorReport{TierLine: tier}
	add := func(name string, ok bool, fatal bool, detail string) {
		report.Checks = append(report.Checks, DoctorCheck{Name: name, OK: ok, Detail: detail, Fatal: fatal})
	}

	if s.db == nil {
		add("store present", false, false, fmt.Sprintf("no index file at %s; run: fisk-ai knowledge index", s.dbPath))
	} else {
		add("store present", true, false, s.dbPath)
		s.doctorDBChecks(ctx, add)
	}

	s.doctorPathChecks(paths, add)
	s.doctorEmbeddingChecks(ctx, add)

	return report, nil
}

// doctorDBChecks runs the checks that need an open index: FTS5 support, WAL mode,
// and write permission on the file.
func (s *Store) doctorDBChecks(ctx context.Context, add func(string, bool, bool, string)) {
	if err := s.verifyFTS5(ctx); err != nil {
		add("FTS5 compiled in", false, true, err.Error())
	} else {
		add("FTS5 compiled in", true, true, "")
	}

	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		add("WAL journal mode", false, false, err.Error())
	} else {
		add("WAL journal mode", mode == "wal", false, "journal_mode="+mode)
	}

	if fi, err := os.Stat(s.dbPath); err != nil {
		add("index writable", false, true, err.Error())
	} else {
		writable := fi.Mode().Perm()&0o200 != 0
		add("index writable", writable, false, fi.Mode().Perm().String())
	}
}

// doctorPathChecks confirms each configured index path resolves on disk.
func (s *Store) doctorPathChecks(paths []string, add func(string, bool, bool, string)) {
	if len(paths) == 0 {
		add("index paths resolve", true, false, "no paths configured (pass a path to knowledge index)")
		return
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			add("index path "+p, false, false, err.Error())
		} else {
			add("index path "+p, true, false, "")
		}
	}
}

// doctorEmbeddingChecks probes the embeddings server and reconciles it with the
// manifest, but only when the vector tier is configured; otherwise it records the
// informational lexical-only line. None of these are fatal.
func (s *Store) doctorEmbeddingChecks(ctx context.Context, add func(string, bool, bool, string)) {
	if s.emb == nil {
		add("embeddings", true, false, "not configured (lexical-only)")
		return
	}

	dim, err := s.emb.Dim(ctx)
	if err != nil {
		add("embeddings reachable", false, false, err.Error())
		return
	}
	add("embeddings reachable", true, false, fmt.Sprintf("model=%s dim=%d", s.emb.Model(), dim))

	if s.db == nil {
		return
	}
	meta, err := s.readMeta(ctx)
	if err != nil {
		add("manifest matches model", false, false, err.Error())
		return
	}
	if meta.Model == "" {
		add("manifest matches model", false, false, "index is lexical-only; run: fisk-ai knowledge index --reindex")
		return
	}
	mismatch := meta.Model != s.emb.Model() || meta.Dimension != dim ||
		meta.QueryPrefix != s.emb.QueryPrefix() || meta.DocumentPrefix != s.emb.DocumentPrefix()
	if mismatch {
		add("manifest matches model", false, false,
			fmt.Sprintf("index built with model=%s dim=%d; config requests model=%s dim=%d; run: fisk-ai knowledge index --reindex",
				meta.Model, meta.Dimension, s.emb.Model(), dim))
		return
	}
	add("manifest matches model", true, false, "")
}
