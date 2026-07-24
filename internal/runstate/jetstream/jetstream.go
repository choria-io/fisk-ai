//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package jetstream is the NATS JetStream session store backend: each run is a
// sequence of records on a pre-existing JetStream stream, one record per subject
// (<prefix>.<run>.<seq>, with the meta record at <prefix>.<run>._meta). The stream
// is configured MaxMsgsPerSubject=1 with discard-new-per-subject so a record subject
// is write-once and immutable, and appends are fenced against the whole run's tail so
// two writers of the same run cannot interleave. The record body is byte-identical to
// a file-backend journal line, so a run migrates between backends unchanged.
//
// Importing this package registers the backend under runstate.BackendJetStream, so a
// program links it in by importing it (usually for its side effect). It holds no
// exported API beyond that registration. The operator owns the stream: this backend
// binds to it and never creates it, and derives its subject prefix from the single
// wildcard subject the stream binds.
package jetstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/choria-io/fisk-ai/internal/runstate"
)

func init() {
	runstate.Register(runstate.BackendJetStream, newStore, runstate.RequiresNats())
}

const (
	// bindTimeout bounds binding to the stream and reading its configuration at
	// construction, so a wrong stream name or an unreachable JetStream surfaces at run
	// start rather than hanging.
	bindTimeout = 10 * time.Second

	// opTimeout bounds a single store or journal operation's control-plane calls
	// (existence checks, publishes, purges, sizing), so a stalled JetStream fails the
	// operation rather than blocking a run indefinitely.
	opTimeout = 15 * time.Second

	// fetchWait bounds one Fetch when reading a run's records. The records are already
	// durably stored, so a fetch returns them promptly; the wait only bounds the tail
	// case where a snapshot count and the delivered set momentarily disagree.
	fetchWait = 5 * time.Second

	// metaToken is the last subject token of the meta record (seq 1). A leading
	// underscore keeps it distinct from a numeric seq token, so parseSeqToken never
	// confuses it with a record subject.
	metaToken = "_meta"

	// recordFloorBytes is a conservative lower bound on a stream's max message size:
	// below it even a small meta record could not be stored, so a stream capped this
	// low is a construction failure rather than a run that fails on its first write.
	// It is deliberately modest; a large assistant record needs a far higher cap (or an
	// unlimited one), which is the operator's call, so the floor only rejects a stream
	// that cannot hold a session at all.
	recordFloorBytes = 4096
)

// options is the typed shape of the jetstream backend's session options.
type options struct {
	// Stream is the JetStream stream run records are stored on. It is required and
	// must already exist: the backend binds to it and never creates it, so the
	// operator owns the stream's durability and retention policy. The run subject
	// prefix is derived from the stream's own wildcard subject, not configured here.
	Stream string `json:"stream"`
}

// decodeOptions strictly decodes the backend options. A stdlib decoder with
// DisallowUnknownFields catches a mistyped option key the same way the config layer
// catches a mistyped top-level key.
func decodeOptions(raw json.RawMessage) (options, error) {
	var opts options
	if len(raw) == 0 {
		return opts, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&opts); err != nil {
		return opts, fmt.Errorf("invalid jetstream session options: %w", err)
	}

	return opts, nil
}

// newStore is the runstate.Factory for the jetstream backend: it decodes the options,
// binds to the existing stream over the borrowed NATS connection, derives the run
// subject prefix from the stream's wildcard subject, and validates that the stream
// cannot silently lose or mutate a run record. A construction failure surfaces here at
// run start rather than on the first append.
func newStore(env runstate.RuntimeEnv, raw json.RawMessage) (runstate.Store, error) {
	opts, err := decodeOptions(raw)
	if err != nil {
		return nil, err
	}
	if opts.Stream == "" {
		return nil, fmt.Errorf("jetstream session: options.stream is required (the JetStream stream name); the stream must already exist")
	}

	nc := env.Nats
	if nc == nil {
		return nil, fmt.Errorf("jetstream session: requires a NATS connection but none is configured; set nats_context in the config to a context created with `nats context add`")
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream session: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), bindTimeout)
	defer cancel()

	stream, err := js.Stream(ctx, opts.Stream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil, fmt.Errorf("jetstream session: stream %q does not exist; create it first, e.g. `nats stream add %s --subjects '<prefix>.>' --max-msgs-per-subject=1 --discard=new --discard-per-subject` (choose any single-token <prefix>)", opts.Stream, opts.Stream)
	}
	if err != nil {
		return nil, fmt.Errorf("jetstream session: binding stream %q: %w", opts.Stream, err)
	}

	info, err := stream.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("jetstream session: reading configuration of stream %q: %w", opts.Stream, err)
	}

	prefix, err := derivePrefix(info.Config.Subjects)
	if err != nil {
		return nil, fmt.Errorf("jetstream session: stream %q %w", opts.Stream, err)
	}

	if err := checkStreamConfig(info.Config, opts.Stream); err != nil {
		return nil, err
	}

	return &store{js: js, stream: stream, streamName: opts.Stream, prefix: prefix}, nil
}

// derivePrefix resolves the run subject prefix from the stream's bound subjects.
// Exactly one bound subject may carry a wildcard, and it must be a clean <prefix>.>
// (a terminal '>' token, a non-empty prefix, and no other wildcard token): that
// subject is where runs live and its prefix names their keyspace. Any other bound
// subjects are literal and belong to the operator; they are ignored. A stream with no
// wildcard subject, more than one, or a malformed one is rejected so the prefix is
// always unambiguous.
func derivePrefix(subjects []string) (string, error) {
	var wildcards []string
	for _, s := range subjects {
		if subjectHasWildcard(s) {
			wildcards = append(wildcards, s)
		}
	}

	switch len(wildcards) {
	case 0:
		return "", fmt.Errorf("binds no wildcard subject; it must bind exactly one subject of the form <prefix>.> so runs can be stored under it")
	case 1:
		// The sole wildcard subject names the run keyspace; validate its shape below.
	default:
		return "", fmt.Errorf("binds %d wildcard subjects (%s); it must bind exactly one, of the form <prefix>.>, so the run prefix is unambiguous", len(wildcards), strings.Join(wildcards, ", "))
	}

	subject := wildcards[0]
	tokens := strings.Split(subject, ".")
	if tokens[len(tokens)-1] != ">" {
		return "", fmt.Errorf("wildcard subject %q must end in a terminal '>' as <prefix>.>; a '*' wildcard does not bound the run and seq tokens", subject)
	}

	prefixTokens := tokens[:len(tokens)-1]
	if len(prefixTokens) == 0 {
		return "", fmt.Errorf("wildcard subject %q has an empty prefix; it must be <prefix>.> with a non-empty prefix", subject)
	}
	for _, t := range prefixTokens {
		if t == "" {
			return "", fmt.Errorf("wildcard subject %q has an empty token; it must be a clean <prefix>.>", subject)
		}
		if t == "*" || t == ">" {
			return "", fmt.Errorf("wildcard subject %q must be a clean <prefix>.> with no other wildcard tokens", subject)
		}
	}

	return strings.Join(prefixTokens, "."), nil
}

// subjectHasWildcard reports whether any token of subject is a NATS wildcard ('*' or
// '>'), the test for whether a bound subject is the run keyspace or a literal operator
// subject to ignore.
func subjectHasWildcard(subject string) bool {
	for _, t := range strings.Split(subject, ".") {
		if t == "*" || t == ">" {
			return true
		}
	}

	return false
}

// checkStreamConfig rejects a stream that would silently lose, expire, or mutate a run
// record. Each record subject must be write-once (MaxMsgsPerSubject=1 with
// discard-new-per-subject so a re-publish is refused, not allowed to drop the original),
// the run must not expire (no MaxAge), and the size cap must be able to hold a record.
// Each is a construction failure with an actionable fix, not a degraded run.
func checkStreamConfig(cfg jetstream.StreamConfig, name string) error {
	if cfg.MaxMsgsPerSubject != 1 {
		return fmt.Errorf("jetstream session: stream %q has max messages per subject = %d; it must be 1 so each run record is write-once and immutable. Recreate or edit it with --max-msgs-per-subject=1", name, cfg.MaxMsgsPerSubject)
	}
	if cfg.Discard != jetstream.DiscardNew {
		return fmt.Errorf("jetstream session: stream %q discard policy is %s, not DiscardNew; it must discard new writes at a full subject rather than dropping an existing record. Recreate or edit it with --discard=new", name, cfg.Discard)
	}
	if !cfg.DiscardNewPerSubject {
		return fmt.Errorf("jetstream session: stream %q does not discard new per subject; it must so the per-subject limit rejects a re-publish of a record. Recreate or edit it with --discard-per-subject", name)
	}
	if cfg.MaxAge != 0 {
		return fmt.Errorf("jetstream session: stream %q has a %s max age; stored runs would silently expire. Recreate the stream without a max age for durable sessions", name, cfg.MaxAge)
	}
	if cfg.MaxMsgSize > 0 && int64(cfg.MaxMsgSize) < recordFloorBytes {
		return fmt.Errorf("jetstream session: stream %q max message size is %d bytes, below the %d byte session record floor; a run record would fail to write. Recreate or edit it with --max-msg-size=-1 (unlimited) or at least %d", name, cfg.MaxMsgSize, recordFloorBytes, recordFloorBytes)
	}

	return nil
}

// store is the JetStream-backed Store. It binds a pre-existing stream and never closes
// the borrowed NATS connection behind it: the connection is owned by the caller that
// provisioned the RuntimeEnv.
type store struct {
	js         jetstream.JetStream
	stream     jetstream.Stream
	streamName string
	prefix     string
}

// metaSubject is the subject of a run's meta record (seq 1).
func (s *store) metaSubject(id string) string {
	return s.prefix + "." + id + "." + metaToken
}

// runWildcard matches every record of a run, for tailing, reading, and purging.
func (s *store) runWildcard(id string) string {
	return s.prefix + "." + id + ".>"
}

// subjectForSeq is the subject a record at seq is stored under: the meta subject for
// seq 1, else the numeric seq token. The caller validates id first, so the result is
// always a legal NATS subject.
func (s *store) subjectForSeq(id string, seq uint64) string {
	if seq == 1 {
		return s.metaSubject(id)
	}

	return fmt.Sprintf("%s.%s.%d", s.prefix, id, seq)
}

// parseSeqToken maps the last token of a record subject back to its record seq: the
// meta token is seq 1, else the numeric token is parsed directly.
func parseSeqToken(subject string) (uint64, error) {
	token := subject
	if idx := strings.LastIndex(subject, "."); idx >= 0 {
		token = subject[idx+1:]
	}
	if token == metaToken {
		return 1, nil
	}

	return strconv.ParseUint(token, 10, 64)
}

// newNonce mints a per-open random nonce. It tags every record this open publishes
// (<nonce>-<seq> as the Nats-Msg-Id) so a lost-ack retry can recognize its own record
// on the subject and adopt it, distinguishing that from another writer's record.
func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jetstream session: generating journal nonce: %w", err)
	}

	return hex.EncodeToString(b), nil
}

// Create implements runstate.Store.
func (s *store) Create(id string, meta runstate.MetaRecord) (runstate.Journal, error) {
	err := runstate.ValidateID(id)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	_, err = s.stream.GetLastMsgForSubject(ctx, s.metaSubject(id))
	if err == nil {
		return nil, fmt.Errorf("%w: %q", runstate.ErrExists, id)
	}
	if !errors.Is(err, jetstream.ErrMsgNotFound) {
		return nil, fmt.Errorf("jetstream session: checking for existing run %q: %w", id, err)
	}

	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	j := &journal{store: s, id: id, nonce: nonce}

	// The meta append is fenced on an empty run (tailStreamSeq 0), so a racing creator
	// loses with 10071, which Append maps to ErrExists on the seq-1 subject.
	err = j.Append(1, runstate.Record{Seq: 1, Protocol: runstate.MetaProtocol, Meta: &meta})
	if err != nil {
		return nil, err
	}

	return j, nil
}

// Open implements runstate.Store.
func (s *store) Open(id string) (runstate.Journal, error) {
	err := runstate.ValidateID(id)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	// The last message on the run wildcard is the highest-stream-seq record, which is
	// the last appended (records are published in seq order). Its stream sequence seeds
	// the append fence and its subject seq seeds the journal's last seq. Its absence is
	// a missing run.
	last, err := s.stream.GetLastMsgForSubject(ctx, s.runWildcard(id))
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return nil, fmt.Errorf("%w: %q", runstate.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("jetstream session: opening run %q: %w", id, err)
	}

	seq, err := parseSeqToken(last.Subject)
	if err != nil {
		return nil, fmt.Errorf("jetstream session: run %q has an unparsable record subject %q: %w", id, last.Subject, err)
	}

	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}

	return &journal{store: s, id: id, nonce: nonce, lastSeq: seq, tailStreamSeq: last.Sequence}, nil
}

// Load implements runstate.Store.
func (s *store) Load(id string) (*runstate.RunState, error) {
	err := runstate.ValidateID(id)
	if err != nil {
		return nil, err
	}

	recs, err := s.records(id)
	if err != nil {
		return nil, err
	}

	return runstate.Fold(recs)
}

// List implements runstate.Store.
func (s *store) List() ([]runstate.RunInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	// A run id has no dots, so '<prefix>.*._meta' matches exactly the meta subject of
	// every run and nothing else. The filtered stream info returns those subjects,
	// which enumerate the runs without reading any record body.
	info, err := s.stream.Info(ctx, jetstream.WithSubjectFilter(s.prefix+".*."+metaToken))
	if err != nil {
		return nil, fmt.Errorf("jetstream session: listing runs on stream %q: %w", s.streamName, err)
	}

	var out []runstate.RunInfo
	for subject := range info.State.Subjects {
		id, ok := s.runIDFromMetaSubject(subject)
		if !ok {
			continue
		}

		recs, err := s.records(id)
		if err != nil || len(recs) == 0 || recs[0].Meta == nil {
			continue
		}
		rs, err := runstate.Fold(recs)
		if err != nil {
			continue
		}

		ri := runstate.RunInfo{
			RunID:   rs.RunID,
			Created: recs[0].Meta.Created,
			Model:   rs.Fingerprint.Model,
			Prompt:  rs.Prompt,
		}
		if rs.Terminal != nil {
			ri.Terminal = rs.Terminal.Reason
		}
		if last, err := s.stream.GetLastMsgForSubject(ctx, s.runWildcard(id)); err == nil {
			ri.Updated = last.Time
		}
		out = append(out, ri)
	}

	return out, nil
}

// Delete implements runstate.Store. Purging the run wildcard removes every record of
// the run and is idempotent: purging a run with no records removes nothing and
// succeeds.
func (s *store) Delete(id string) error {
	err := runstate.ValidateID(id)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	err = s.stream.Purge(ctx, jetstream.WithPurgeSubject(s.runWildcard(id)))
	if err != nil {
		return fmt.Errorf("jetstream session: deleting run %q: %w", id, err)
	}

	return nil
}

// runIDFromMetaSubject extracts the run id from a meta subject '<prefix>.<id>._meta',
// returning false for a subject that is not one or whose id is not valid.
func (s *store) runIDFromMetaSubject(subject string) (string, bool) {
	inner := strings.TrimPrefix(subject, s.prefix+".")
	if inner == subject {
		return "", false
	}
	id := strings.TrimSuffix(inner, "."+metaToken)
	if id == inner {
		return "", false
	}
	if runstate.ValidateID(id) != nil {
		return "", false
	}

	return id, true
}

// records reads every record of a run in seq order. It requires the run's meta subject
// to be present (its absence is ErrNotFound, matching the file backend before it reads
// a journal), sizes the read from a single filtered stream-info snapshot, then reads
// exactly that many records through an ordered consumer over the run wildcard.
func (s *store) records(id string) ([]runstate.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	info, err := s.stream.Info(ctx, jetstream.WithSubjectFilter(s.runWildcard(id)))
	if err != nil {
		return nil, fmt.Errorf("jetstream session: reading run %q: %w", id, err)
	}
	// The meta record (seq 1) is written first and never removed, so a run exists iff
	// its meta subject is present.
	if info.State.Subjects[s.metaSubject(id)] == 0 {
		return nil, fmt.Errorf("%w: %q", runstate.ErrNotFound, id)
	}

	var pending uint64
	for _, n := range info.State.Subjects {
		pending += n
	}

	cons, err := s.stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{s.runWildcard(id)},
	})
	if err != nil {
		return nil, fmt.Errorf("jetstream session: reading run %q: %w", id, err)
	}

	records := make([]runstate.Record, 0, pending)
	for uint64(len(records)) < pending {
		batch, err := cons.Fetch(int(pending)-len(records), jetstream.FetchMaxWait(fetchWait))
		if err != nil {
			return nil, fmt.Errorf("jetstream session: fetching records for run %q: %w", id, err)
		}

		got := 0
		for msg := range batch.Messages() {
			got++
			var rec runstate.Record
			if err := json.Unmarshal(msg.Data(), &rec); err != nil {
				return nil, fmt.Errorf("%w: run %q record %q: %w", runstate.ErrCorrupt, id, msg.Subject(), err)
			}
			records = append(records, rec)
		}
		if err := batch.Error(); err != nil {
			return nil, fmt.Errorf("jetstream session: reading records for run %q: %w", id, err)
		}
		// No progress within the wait: the stored set is smaller than the snapshot
		// implied (a concurrent write or purge raced the read). Stop rather than loop
		// forever; the records read so far are a coherent prefix.
		if got == 0 {
			break
		}
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })

	return records, nil
}

// journal is the append handle for a single run. It is not safe for concurrent use, as
// the Journal contract states; the Store guards each run's journal.
type journal struct {
	store *store
	id    string
	// nonce tags this open's records so a lost-ack retry recognizes its own write.
	nonce string
	// lastSeq is the highest record seq durably stored by this journal, advanced only
	// after a record is acknowledged, so a failed publish re-appends the same seq.
	lastSeq uint64
	// tailStreamSeq is the stream sequence of the run's last record, the value the next
	// append fences on so no other writer can interleave.
	tailStreamSeq uint64
}

// Append implements runstate.Journal. The dup/gap decision is the shared
// runstate.CheckAppend contract. The write is a fenced publish: it asserts the run's
// tail stream sequence over the run wildcard, so a stale writer (its tail moved under
// it) is rejected with 10071 rather than interleaving. A rejection is disambiguated by
// reading the target subject: our own record already there is a lost ack to adopt,
// anything else is another writer holding the run (ErrLocked), or on the meta subject a
// concurrent creator (ErrExists).
func (j *journal) Append(seq uint64, rec runstate.Record) error {
	skip, err := runstate.CheckAppend(j.lastSeq, seq)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	rec.Seq = seq

	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("jetstream session: marshaling record %d of run %q: %w", seq, j.id, err)
	}

	subject := j.store.subjectForSeq(j.id, seq)
	msgID := fmt.Sprintf("%s-%d", j.nonce, seq)

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	ack, err := j.store.js.Publish(ctx, subject, body,
		jetstream.WithMsgID(msgID),
		jetstream.WithExpectLastSequenceForSubject(j.tailStreamSeq, j.store.runWildcard(j.id)))
	if err == nil {
		j.tailStreamSeq = ack.Sequence
		j.lastSeq = seq
		return nil
	}
	if !isErrWrongLastSequence(err) {
		return fmt.Errorf("jetstream session: appending record %d to run %q: %w", seq, j.id, err)
	}

	// The fence failed: the run's tail is not where we left it. Read the target subject
	// to tell our own already-landed record (a lost ack) from another writer's.
	existing, gErr := j.store.stream.GetLastMsgForSubject(ctx, subject)
	if gErr != nil && !errors.Is(gErr, jetstream.ErrMsgNotFound) {
		return fmt.Errorf("jetstream session: record %d of run %q was rejected and its state could not be read back: %w", seq, j.id, gErr)
	}
	if gErr == nil && existing.Header.Get(nats.MsgIdHdr) == msgID {
		// Our earlier publish landed without our seeing the ack: adopt its position.
		j.tailStreamSeq = existing.Sequence
		j.lastSeq = seq
		return nil
	}

	if seq == 1 {
		// The meta subject is occupied by another creator: the run already exists.
		return fmt.Errorf("%w: %q", runstate.ErrExists, j.id)
	}

	return fmt.Errorf("%w: run %q on stream %q was advanced by another writer, so record %d was safely rejected without overwriting anything; another process holds this run (inspect it with `nats stream subjects %s '%s'`)",
		runstate.ErrLocked, j.id, j.store.streamName, seq, j.store.streamName, j.store.runWildcard(j.id))
}

// Records implements runstate.Journal.
func (j *journal) Records() ([]runstate.Record, error) {
	return j.store.records(j.id)
}

// LastSeq implements runstate.Journal.
func (j *journal) LastSeq() uint64 {
	return j.lastSeq
}

// Close implements runstate.Journal. There is no lock to release and the NATS
// connection is borrowed, so there is nothing to close.
func (j *journal) Close() error {
	return nil
}

// isErrWrongLastSequence reports whether err is JetStream's wrong-last-sequence error
// (10071), returned when a publish precondition (here the per-run tail fence) does not
// hold. It is the sole error the append path disambiguates.
func isErrWrongLastSequence(err error) bool {
	var apiErr *jetstream.APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	return apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence
}
