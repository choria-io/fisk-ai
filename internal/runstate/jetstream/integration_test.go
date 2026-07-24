//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package jetstream

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/segmentio/ksuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/runstate/file"
)

// runJetStream starts an embedded JetStream-enabled NATS server on a random port and
// returns a client connection. Both are torn down when the spec ends. The Describe is
// labeled Integration so the unit suite (ginkgo --skip Integration) does not run it.
func runJetStream() *nats.Conn {
	GinkgoHelper()

	ns, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: GinkgoT().TempDir()})
	Expect(err).NotTo(HaveOccurred())

	go ns.Start()
	Expect(ns.ReadyForConnections(10 * time.Second)).To(BeTrue())
	DeferCleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(nc.Close)

	return nc
}

func newID() string {
	return ksuid.New().String()
}

func assistantRec(iter int64, toolIDs ...string) runstate.Record {
	content := []llm.ContentBlock{{Text: &llm.TextBlock{Text: "working"}}}
	for _, id := range toolIDs {
		content = append(content, llm.ContentBlock{ToolUse: &llm.ToolUseBlock{ID: id, Name: "shell", Input: json.RawMessage(`{"x":1}`)}})
	}

	return runstate.Record{
		Protocol:  runstate.AssistantProtocol,
		Assistant: &runstate.AssistantRecord{Iteration: iter, Message: llm.Message{Role: llm.RoleAssistant, Content: content}, InTokens: 10, OutTokens: 5},
	}
}

func toolResultRec(id string) runstate.Record {
	return runstate.Record{
		Protocol:   runstate.ToolResultProtocol,
		ToolResult: &runstate.ToolResultRecord{ToolUseID: id, Result: llm.ToolResultBlock{ToolUseID: id, Content: "ok"}},
	}
}

// goodStream is a stream configuration the backend accepts: a single <prefix>.>
// wildcard, write-once subjects, and no expiry.
func goodStream(name string, subjects ...string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:                 name,
		Subjects:             subjects,
		MaxMsgsPerSubject:    1,
		Discard:              jetstream.DiscardNew,
		DiscardNewPerSubject: true,
	}
}

var _ = Describe("Integration: jetstream session", func() {
	var (
		ctx context.Context
		nc  *nats.Conn
		js  jetstream.JetStream
	)

	newStoreFor := func(stream string) (runstate.Store, error) {
		return newStore(runstate.RuntimeEnv{Nats: nc}, []byte(fmt.Sprintf(`{"stream":%q}`, stream)))
	}

	createStream := func(cfg jetstream.StreamConfig) {
		GinkgoHelper()
		_, err := js.CreateStream(ctx, cfg)
		Expect(err).ToNot(HaveOccurred())
	}

	newMeta := func(id string) runstate.MetaRecord {
		return runstate.MetaRecord{
			Version:     runstate.Version,
			RunID:       id,
			Created:     time.Unix(1700000000, 0).UTC(),
			Prompt:      "hello",
			Fingerprint: runstate.Fingerprint{Model: "claude-opus-4-8"},
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		nc = runJetStream()
		var err error
		js, err = jetstream.New(nc)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("binding", func() {
		It("Should fail when the stream does not exist", func() {
			_, err := newStoreFor("MISSING")
			Expect(err).To(MatchError(ContainSubstring("does not exist")))
		})

		It("Should reject a stream that binds no wildcard subject", func() {
			createStream(jetstream.StreamConfig{Name: "LIT", Subjects: []string{"literal.subject"}})
			_, err := newStoreFor("LIT")
			Expect(err).To(MatchError(ContainSubstring("binds no wildcard subject")))
		})

		It("Should reject a max-msgs-per-subject other than 1", func() {
			createStream(jetstream.StreamConfig{Name: "MANY", Subjects: []string{"runs.>"}, MaxMsgsPerSubject: 5, Discard: jetstream.DiscardNew, DiscardNewPerSubject: true})
			_, err := newStoreFor("MANY")
			Expect(err).To(MatchError(ContainSubstring("max messages per subject")))
		})

		It("Should reject a discard policy other than DiscardNew", func() {
			createStream(jetstream.StreamConfig{Name: "OLD", Subjects: []string{"runs.>"}, MaxMsgsPerSubject: 1, Discard: jetstream.DiscardOld})
			_, err := newStoreFor("OLD")
			Expect(err).To(MatchError(ContainSubstring("not DiscardNew")))
		})

		It("Should reject a stream without discard-new-per-subject", func() {
			createStream(jetstream.StreamConfig{Name: "NOPS", Subjects: []string{"runs.>"}, MaxMsgsPerSubject: 1, Discard: jetstream.DiscardNew, DiscardNewPerSubject: false})
			_, err := newStoreFor("NOPS")
			Expect(err).To(MatchError(ContainSubstring("discard new per subject")))
		})

		It("Should reject a stream with a max age", func() {
			cfg := goodStream("AGED", "runs.>")
			cfg.MaxAge = time.Hour
			createStream(cfg)
			_, err := newStoreFor("AGED")
			Expect(err).To(MatchError(ContainSubstring("max age")))
		})

		It("Should reject a max message size below the record floor", func() {
			cfg := goodStream("TINY", "runs.>")
			cfg.MaxMsgSize = 1024
			createStream(cfg)
			_, err := newStoreFor("TINY")
			Expect(err).To(MatchError(ContainSubstring("max message size")))
		})

		It("Should derive the run prefix from a well-formed stream", func() {
			createStream(goodStream("SESSIONS", "ops.audit", "runs.>"))
			s, err := newStoreFor("SESSIONS")
			Expect(err).ToNot(HaveOccurred())
			Expect(s.(*store).prefix).To(Equal("runs"))
		})
	})

	Describe("CRUD and resume", func() {
		var store runstate.Store

		BeforeEach(func() {
			createStream(goodStream("SESSIONS", "runs.>"))
			var err error
			store, err = newStoreFor("SESSIONS")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Should create, append, and fold back a run", func() {
			id := newID()
			j, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			Expect(j.Append(2, assistantRec(0, "tu_1"))).To(Succeed())
			Expect(j.Append(3, toolResultRec("tu_1"))).To(Succeed())
			Expect(j.LastSeq()).To(Equal(uint64(3)))
			Expect(j.Close()).To(Succeed())

			rs, err := store.Load(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(rs.RunID).To(Equal(id))
			Expect(rs.Messages).To(HaveLen(3))
			Expect(rs.NextIteration).To(Equal(int64(1)))
			Expect(rs.Counters.ToolCalls).To(Equal(int64(1)))
		})

		It("Should refuse to create a run that already exists", func() {
			id := newID()
			j, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			Expect(j.Close()).To(Succeed())

			_, err = store.Create(id, newMeta(id))
			Expect(err).To(MatchError(runstate.ErrExists))
		})

		It("Should return ErrNotFound for an absent run", func() {
			id := newID()
			_, err := store.Open(id)
			Expect(err).To(MatchError(runstate.ErrNotFound))
			_, err = store.Load(id)
			Expect(err).To(MatchError(runstate.ErrNotFound))
		})

		It("Should open a meta-only run and continue its sequence", func() {
			id := newID()
			j, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			Expect(j.Close()).To(Succeed())

			j2, err := store.Open(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(j2.LastSeq()).To(Equal(uint64(1)))
			Expect(j2.Append(2, assistantRec(0))).To(Succeed())
			Expect(j2.Close()).To(Succeed())

			rs, err := store.Load(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(rs.Counters.LlmCalls).To(Equal(int64(1)))
		})

		It("Should treat a duplicate seq as an idempotent no-op and reject gaps", func() {
			id := newID()
			j, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			defer j.Close()

			Expect(j.Append(2, assistantRec(0, "tu_1"))).To(Succeed())
			// Re-append the same seq (crash-retry): no error, no duplicate.
			Expect(j.Append(2, assistantRec(0, "tu_1"))).To(Succeed())
			// A seq that skips ahead is a gap.
			Expect(j.Append(5, toolResultRec("tu_1"))).To(MatchError(runstate.ErrSeqGap))

			recs, err := j.Records()
			Expect(err).ToNot(HaveOccurred())
			Expect(recs).To(HaveLen(2))
		})

		It("Should adopt its own lost-ack record instead of duplicating it", func() {
			id := newID()
			jr, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			j := jr.(*journal)

			// Simulate a landed-but-unacked publish of record 2: publish it directly with
			// the journal's own msg id and the fence it would have used, so the journal's
			// tail view is now stale (it never saw the ack and did not advance).
			rec := assistantRec(0, "tu_1")
			rec.Seq = 2
			body, err := json.Marshal(rec)
			Expect(err).ToNot(HaveOccurred())
			_, err = js.Publish(ctx, j.store.subjectForSeq(id, 2), body,
				jetstream.WithMsgID(fmt.Sprintf("%s-%d", j.nonce, 2)),
				jetstream.WithExpectLastSequenceForSubject(j.tailStreamSeq, j.store.runWildcard(id)))
			Expect(err).ToNot(HaveOccurred())

			// The retry hits the fence, recognizes its own record, and adopts it: no
			// error and no duplicate, and the sequence continues cleanly.
			Expect(j.Append(2, rec)).To(Succeed())
			Expect(j.LastSeq()).To(Equal(uint64(2)))
			Expect(j.Append(3, toolResultRec("tu_1"))).To(Succeed())

			recs, err := j.Records()
			Expect(err).ToNot(HaveOccurred())
			Expect(recs).To(HaveLen(3))
		})

		It("Should fence a second writer out with ErrLocked", func() {
			id := newID()
			jA, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			Expect(jA.Append(2, assistantRec(0, "tu_1"))).To(Succeed())

			jB, err := store.Open(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(jB.LastSeq()).To(Equal(uint64(2)))

			// Writer A advances the run, moving the tail under B.
			Expect(jA.Append(3, toolResultRec("tu_1"))).To(Succeed())

			// B's next append collides with A's tail move and is safely rejected.
			err = jB.Append(3, toolResultRec("tu_1"))
			Expect(err).To(MatchError(runstate.ErrLocked))
		})

		It("Should list runs with their metadata", func() {
			idA, idB := newID(), newID()
			jA, err := store.Create(idA, newMeta(idA))
			Expect(err).ToNot(HaveOccurred())
			Expect(jA.Close()).To(Succeed())
			jB, err := store.Create(idB, newMeta(idB))
			Expect(err).ToNot(HaveOccurred())
			Expect(jB.Append(2, assistantRec(0))).To(Succeed())
			Expect(jB.Close()).To(Succeed())

			infos, err := store.List()
			Expect(err).ToNot(HaveOccurred())
			Expect(infos).To(HaveLen(2))

			ids := []string{infos[0].RunID, infos[1].RunID}
			Expect(ids).To(ConsistOf(idA, idB))
			for _, in := range infos {
				Expect(in.Model).To(Equal("claude-opus-4-8"))
				Expect(in.Prompt).To(Equal("hello"))
				Expect(in.Created).ToNot(BeZero())
				Expect(in.Updated).ToNot(BeZero())
			}
		})

		It("Should delete a run idempotently", func() {
			id := newID()
			j, err := store.Create(id, newMeta(id))
			Expect(err).ToNot(HaveOccurred())
			Expect(j.Append(2, assistantRec(0))).To(Succeed())
			Expect(j.Close()).To(Succeed())

			Expect(store.Delete(id)).To(Succeed())
			_, err = store.Load(id)
			Expect(err).To(MatchError(runstate.ErrNotFound))

			// Purging an absent run is a no-op.
			Expect(store.Delete(id)).To(Succeed())
		})

		It("Should fold identically to the file backend", func() {
			id := newID()
			meta := newMeta(id)
			appends := []struct {
				seq uint64
				rec runstate.Record
			}{
				{2, assistantRec(0, "tu_1")},
				{3, toolResultRec("tu_1")},
				{4, assistantRec(1)},
			}

			jj, err := store.Create(id, meta)
			Expect(err).ToNot(HaveOccurred())
			for _, a := range appends {
				Expect(jj.Append(a.seq, a.rec)).To(Succeed())
			}
			Expect(jj.Close()).To(Succeed())
			jsRS, err := store.Load(id)
			Expect(err).ToNot(HaveOccurred())

			fstore, err := file.NewFileStore(GinkgoT().TempDir())
			Expect(err).ToNot(HaveOccurred())
			fj, err := fstore.Create(id, meta)
			Expect(err).ToNot(HaveOccurred())
			for _, a := range appends {
				Expect(fj.Append(a.seq, a.rec)).To(Succeed())
			}
			Expect(fj.Close()).To(Succeed())
			fileRS, err := fstore.Load(id)
			Expect(err).ToNot(HaveOccurred())

			Expect(jsRS).To(Equal(fileRS))
		})
	})
})
