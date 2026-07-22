//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package jetstream is the NATS JetStream KV memory backend: each memory is a
// value in a pre-existing KV bucket, carrying its one-line description in the same
// YAML frontmatter the file backend uses so a value migrates between backends
// unchanged. Importing this package registers the backend under
// memory.BackendJetStream, so a program links it in by importing it (usually for
// its side effect). It holds no exported API beyond that registration.
package jetstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/choria-io/fisk-ai/internal/memory"
)

func init() {
	memory.Register(memory.BackendJetStream, newStore, memory.RequiresNats())
}

const (
	// bindTimeout bounds binding to the bucket and reading its status at
	// construction, so a wrong bucket name or an unreachable JetStream surfaces at
	// run start rather than hanging.
	bindTimeout = 10 * time.Second

	// prefixSeparator joins the key prefix and the key as "<prefix>.<key>". Dot is a
	// legal memory key character, so a prefixed key stays a legal memory key and a
	// legal NATS KV key.
	prefixSeparator = "."

	// allKeys is the KV subject wildcard matching every key. Joined with the prefix
	// it watches only this store's keyspace; alone it watches the whole bucket.
	allKeys = ">"
)

// options is the typed shape of harness.memory.options for the jetstream backend.
type options struct {
	// Bucket is the KV bucket memories are stored in. It is required and must
	// already exist: the backend binds to it and never creates it, so the operator
	// owns the bucket's durability policy.
	Bucket string `json:"bucket"`

	// Prefix namespaces this agent's keys within the bucket, stored as
	// "<prefix>.<key>". Unset (nil) defaults to the agent identity, mirroring the
	// file backend's memory/<identity> directory so two agents do not share a
	// keyspace by accident. Set it to a shared value for agents that deliberately
	// share memory, or to "" for a flat, unprefixed keyspace. It is a pointer so an
	// omitted prefix (default to identity) is distinct from an explicit empty one.
	Prefix *string `json:"prefix,omitempty"`

	// NoRequireReadBeforeUpdate turns off the read-before-update guard. By default
	// an overwrite must follow a read of the current value in this run and fails
	// with ErrStale otherwise, so a run cannot clobber a memory it has not seen or
	// that changed under it (real lost-update protection over a shared bucket, which
	// the KV revision makes atomic). Set this to allow a blind overwrite, matching
	// the file backend. Like no_index it is a negative switch.
	NoRequireReadBeforeUpdate bool `json:"no_require_read_before_update,omitempty"`
}

// newStore is the memory.Factory for the jetstream backend: it decodes the
// options, resolves the key prefix, binds to the existing bucket over the borrowed
// NATS connection, and validates that the bucket cannot silently lose memories. A
// construction failure surfaces here at run start rather than on the first tool
// call.
func newStore(env memory.RuntimeEnv, identity string, raw json.RawMessage) (memory.Store, error) {
	opts, err := memory.DecodeOptions[options](raw, "jetstream memory")
	if err != nil {
		return nil, err
	}
	if opts.Bucket == "" {
		return nil, fmt.Errorf("jetstream memory: options.bucket is required (the KV bucket name); the bucket must already exist")
	}

	prefix := identity
	if opts.Prefix != nil {
		prefix = *opts.Prefix
	}
	if prefix != "" {
		err := memory.ValidateKey(prefix)
		if err != nil && opts.Prefix == nil {
			return nil, fmt.Errorf("jetstream memory: the agent identity %q cannot be used as a key prefix (%w); set options.prefix to a legal value, or \"\" for a flat keyspace", identity, err)
		}
		if err != nil {
			return nil, fmt.Errorf("jetstream memory: options.prefix %q is invalid: %w", prefix, err)
		}
	}

	nc := env.Nats
	if nc == nil {
		return nil, fmt.Errorf("jetstream memory: requires a NATS connection but none is configured; set nats_context in the config to a context created with `nats context add`")
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream memory: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), bindTimeout)
	defer cancel()

	kv, err := js.KeyValue(ctx, opts.Bucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, fmt.Errorf("jetstream memory: KV bucket %q does not exist; create it first, e.g. `nats kv add %s --history=1 --max-value-size=%d`", opts.Bucket, opts.Bucket, memory.MaxEntryBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("jetstream memory: binding KV bucket %q: %w", opts.Bucket, err)
	}

	if err := checkBucketConfig(ctx, kv, opts.Bucket); err != nil {
		return nil, err
	}

	return &store{
		kv:          kv,
		bucket:      opts.Bucket,
		prefix:      prefix,
		requireRead: !opts.NoRequireReadBeforeUpdate,
		revs:        map[string]uint64{},
	}, nil
}

// checkBucketConfig rejects a bucket that would silently lose memories. Memory is a
// durable store the model reasons over, so a TTL that expires entries or a max
// value size below the entry limit is a construction failure, not a degraded run:
// it fails here at run start just like a missing bucket.
func checkBucketConfig(ctx context.Context, kv jetstream.KeyValue, bucket string) error {
	status, err := kv.Status(ctx)
	if err != nil {
		return fmt.Errorf("jetstream memory: reading status of KV bucket %q: %w", bucket, err)
	}

	if ttl := status.TTL(); ttl != 0 {
		return fmt.Errorf("jetstream memory: KV bucket %q has a %s TTL set; stored memories would silently expire. Recreate the bucket without a TTL for durable memory", bucket, ttl)
	}

	// A non-positive max value size means no cap: NATS maps it to the backing
	// stream's MaxMsgSize, whose unset default is -1 (unlimited). Only a genuine
	// positive cap below the entry limit would truncate a write, so reject just that.
	// The bound is MaxEntryBytes, not MaxContentBytes: the stored value is the
	// serialized entry (the body plus its frontmatter header), so a bucket sized to
	// the body cap alone would reject a full-size memory at write time.
	if maxValue := status.Config().MaxValueSize; maxValue > 0 && int64(maxValue) < memory.MaxEntryBytes {
		return fmt.Errorf("jetstream memory: KV bucket %q max value size is %d bytes, below the %d byte memory entry limit; large memories would fail to write. Recreate the bucket with --max-value-size=%d", bucket, maxValue, memory.MaxEntryBytes, memory.MaxEntryBytes)
	}

	return nil
}

// store is the JetStream KV-backed Store. It binds a pre-existing bucket and never
// closes the borrowed NATS connection behind it: the connection is owned by the
// caller that provisioned the RuntimeEnv.
type store struct {
	kv          jetstream.KeyValue
	bucket      string
	prefix      string
	requireRead bool

	// revs records the KV revision each key was last seen at through Read or a
	// successful write in this run, so an overwrite can be gated on the model having
	// seen the current value (read-before-update). It is per run (the store is built
	// per run) and guarded because the Store contract allows concurrent use.
	mu   sync.Mutex
	revs map[string]uint64

	// countCache holds the entry count for the capacity check, seeded lazily from one
	// keyspace scan and then tracked through this run's own creates and deletes, so a
	// run that writes many memories does not rescan on every create. countCached
	// guards the seed. See currentCount for the best-effort-under-concurrency story.
	countMu     sync.Mutex
	countCache  int
	countCached bool
}

// remember records the revision a key is now known to be at, granting overwrite
// authority for it this run. forget drops that authority.
func (s *store) remember(key string, rev uint64) {
	s.mu.Lock()
	s.revs[key] = rev
	s.mu.Unlock()
}

func (s *store) forget(key string) {
	s.mu.Lock()
	delete(s.revs, key)
	s.mu.Unlock()
}

// knownRevision returns the revision key was last seen at this run and whether it
// was seen at all.
func (s *store) knownRevision(key string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rev, ok := s.revs[key]

	return rev, ok
}

// storageKey maps a memory key to the bucket key, applying the namespace prefix.
// The caller validates key first, so the result is always a legal KV key.
func (s *store) storageKey(key string) string {
	if s.prefix == "" {
		return key
	}

	return s.prefix + prefixSeparator + key
}

func (s *store) Read(ctx context.Context, key string) (string, string, error) {
	if err := memory.ValidateKey(key); err != nil {
		return "", "", err
	}

	entry, err := s.kv.Get(ctx, s.storageKey(key))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return "", "", memory.ErrNotExist
	}
	if err != nil {
		return "", "", fmt.Errorf("reading memory %q: %w", key, err)
	}

	// Record the revision so a later overwrite can prove the model saw this value.
	// Only Read does this: the start-of-run index and List read values to build the
	// key/description index, not on the model's behalf, so they must not grant
	// overwrite authority (seeing a key in the index is not the same as reading it).
	s.remember(key, entry.Revision())

	description, content := memory.Parse(entry.Value())

	return description, content, nil
}

func (s *store) Write(ctx context.Context, key, description, content string, overwrite bool) error {
	description, err := memory.ValidateWrite(key, description, content)
	if err != nil {
		return err
	}

	data, err := memory.Serialize(description, content)
	if err != nil {
		return err
	}

	sk := s.storageKey(key)

	if overwrite {
		return s.overwrite(ctx, key, sk, data)
	}

	// Create is atomic on existence: it fails with ErrKeyExists for a live key but
	// succeeds for a previously deleted (tombstoned) one, which is exactly the
	// create-guard the contract wants. The capacity check ahead of it is, like the
	// file backend's, best-effort under concurrency.
	count, err := s.currentCount(ctx)
	if err != nil {
		return err
	}
	if err := memory.CheckCapacity(count); err != nil {
		return err
	}

	rev, err := s.kv.Create(ctx, sk, data)
	if errors.Is(err, jetstream.ErrKeyExists) {
		return memory.ErrExists
	}
	if err != nil {
		return fmt.Errorf("creating memory %q: %w", key, err)
	}

	// Creating a key grants overwrite authority for it: the model just wrote its
	// value, so it may keep editing it this run without re-reading. It also adds one
	// to the tracked entry count.
	s.remember(key, rev)
	s.adjustCount(1)

	return nil
}

// overwrite replaces an existing memory. With the read-before-update guard on it
// uses a revision-checked Update so it only succeeds when the model read the
// current value in this run and no writer has changed it since; otherwise it is a
// blind Put. Either way it records the new revision so the model can keep editing.
func (s *store) overwrite(ctx context.Context, key, sk string, data []byte) error {
	if !s.requireRead {
		rev, err := s.kv.Put(ctx, sk, data)
		if err != nil {
			return fmt.Errorf("writing memory %q: %w", key, err)
		}
		s.remember(key, rev)

		return nil
	}

	prev, ok := s.knownRevision(key)
	if !ok {
		return fmt.Errorf("%w: memory %q was not read in this run; read it before overwriting", memory.ErrStale, key)
	}

	rev, err := s.kv.Update(ctx, sk, data, prev)
	if isWrongLastSequence(err) {
		// The revision moved: another writer changed the key since it was read. Drop
		// the stale authority so a retry must read the new value first.
		s.forget(key)
		return fmt.Errorf("%w: memory %q changed since it was read; read it again before overwriting", memory.ErrStale, key)
	}
	if err != nil {
		return fmt.Errorf("writing memory %q: %w", key, err)
	}

	s.remember(key, rev)

	return nil
}

// isWrongLastSequence reports whether err is JetStream's revision-mismatch error,
// which Update returns when the key's latest revision is not the expected one.
func isWrongLastSequence(err error) bool {
	var apiErr *jetstream.APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	return apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence
}

func (s *store) Delete(ctx context.Context, key string) (bool, error) {
	if err := memory.ValidateKey(key); err != nil {
		return false, err
	}

	sk := s.storageKey(key)

	// Whether the key existed is best-effort under concurrency: unlike the file
	// backend's single atomic os.Remove, this is a Get then a Delete, so a
	// concurrent deleter can race between them. Delete itself stays idempotent.
	_, err := s.kv.Get(ctx, sk)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("deleting memory %q: %w", key, err)
	}

	if err := s.kv.Delete(ctx, sk); err != nil {
		return false, fmt.Errorf("deleting memory %q: %w", key, err)
	}

	// Drop any tracked revision: a later create is the way back, and it records its
	// own. A stale entry left here could authorize an overwrite of a re-created key.
	// The delete also takes one off the tracked entry count.
	s.forget(key)
	s.adjustCount(-1)

	return true, nil
}

func (s *store) List(ctx context.Context) ([]memory.Item, error) {
	items, err := s.snapshot(ctx, false)
	if err != nil {
		return nil, err
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })

	return items, nil
}

// snapshot streams this store's current entries in a single server-side pass. It
// watches only this store's keyspace (the prefix as a subject filter), so on a
// shared bucket it never transfers another agent's keys, and it reads keys and
// values together rather than a Get per key. With metaOnly it skips the values, for
// counting. A watcher replays every current key and then sends a nil entry once the
// initial state is complete, which ends the pass before the live tail; delete
// markers are excluded and duplicates (which a busy bucket can report) are dropped.
func (s *store) snapshot(ctx context.Context, metaOnly bool) ([]memory.Item, error) {
	opts := []jetstream.WatchOpt{jetstream.IgnoreDeletes()}
	if metaOnly {
		opts = append(opts, jetstream.MetaOnly())
	}

	subject := allKeys
	filter := ""
	if s.prefix != "" {
		subject = s.prefix + prefixSeparator + allKeys
		filter = s.prefix + prefixSeparator
	}

	w, err := s.kv.Watch(ctx, subject, opts...)
	if err != nil {
		return nil, fmt.Errorf("listing memory: %w", err)
	}
	defer func() { _ = w.Stop() }()

	seen := map[string]struct{}{}
	var items []memory.Item
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case entry, ok := <-w.Updates():
			if !ok {
				// The watcher closed before the initial-state marker (a dropped
				// connection or a terminated consumer), so what arrived is an
				// incomplete snapshot. Fail rather than pass a short result off as
				// complete: an undercount would bypass the entry cap and a short list
				// would hide stored memories from the model. A clean end always
				// arrives as the nil entry below.
				return nil, fmt.Errorf("listing memory: watch closed before the snapshot completed")
			}
			if entry == nil {
				// All current values delivered; the live tail is not wanted.
				return items, nil
			}

			key := strings.TrimPrefix(entry.Key(), filter)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}

			description := ""
			if !metaOnly {
				description, _ = memory.Parse(entry.Value())
			}
			items = append(items, memory.Item{Key: key, Description: description})
		}
	}
}

// currentCount returns how many memories this store holds, for the create-time
// entry cap. It seeds a cached count from one keyspace scan the first time it is
// needed and then tracks this run's own creates and deletes (adjustCount), so a run
// that writes many memories scans once rather than on every create. The count is a
// per-run best-effort estimate: a concurrent writer sharing the keyspace is not
// seen, which matches the cap's documented best-effort-under-concurrency semantics
// and still bounds a single runaway run exactly.
func (s *store) currentCount(ctx context.Context) (int, error) {
	s.countMu.Lock()
	cached, c := s.countCached, s.countCache
	s.countMu.Unlock()
	if cached {
		return c, nil
	}

	items, err := s.snapshot(ctx, true)
	if err != nil {
		return 0, err
	}

	s.countMu.Lock()
	if !s.countCached {
		s.countCache = len(items)
		s.countCached = true
	}
	c = s.countCache
	s.countMu.Unlock()

	return c, nil
}

// adjustCount keeps the cached entry count in step with this run's own creates and
// deletes. It is a no-op until currentCount has seeded the cache, so a run that only
// reads or overwrites never scans.
func (s *store) adjustCount(delta int) {
	s.countMu.Lock()
	if s.countCached {
		s.countCache += delta
		if s.countCache < 0 {
			s.countCache = 0
		}
	}
	s.countMu.Unlock()
}
