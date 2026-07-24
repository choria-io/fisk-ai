//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package jetstream

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/runstate"
)

// These specs cover construction failures and the pure prefix/subject helpers, which
// need no NATS server and run in the unit suite.
var _ = Describe("newStore construction", func() {
	It("Should require a stream", func() {
		_, err := newStore(runstate.RuntimeEnv{}, []byte(`{}`))
		Expect(err).To(MatchError(ContainSubstring("options.stream is required")))
	})

	It("Should require a NATS connection", func() {
		_, err := newStore(runstate.RuntimeEnv{}, []byte(`{"stream":"sessions"}`))
		Expect(err).To(MatchError(ContainSubstring("requires a NATS connection")))
	})

	It("Should reject an unknown option key", func() {
		_, err := newStore(runstate.RuntimeEnv{}, []byte(`{"stream":"sessions","nonesuch":"x"}`))
		Expect(err).To(MatchError(ContainSubstring("invalid jetstream session options")))
	})

	It("Should be registered under the jetstream backend name", func() {
		Expect(runstate.Backends()).To(ContainElement(runstate.BackendJetStream))
	})

	It("Should declare that it needs a NATS connection", func() {
		Expect(runstate.NeedsNats(runstate.BackendJetStream)).To(BeTrue())
	})
})

var _ = Describe("derivePrefix", func() {
	DescribeTable("rejects a stream whose subjects cannot name a single run keyspace",
		func(subjects []string, msg string) {
			_, err := derivePrefix(subjects)
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("no wildcard subject", []string{"events.audit"}, "binds no wildcard subject"),
		Entry("an empty subject set", []string{}, "binds no wildcard subject"),
		Entry("two wildcard subjects", []string{"runs.>", "jobs.>"}, "binds 2 wildcard subjects"),
		Entry("a '*' wildcard that does not terminate the subject", []string{"runs.*"}, "must end in a terminal '>'"),
		Entry("a '*' before a literal tail", []string{"runs.*.meta"}, "must end in a terminal '>'"),
		Entry("a bare terminal with no prefix", []string{">"}, "empty prefix"),
		Entry("a '*' inside the prefix", []string{"runs.*.>"}, "no other wildcard tokens"),
		Entry("an empty token inside the prefix", []string{"runs..>"}, "empty token"),
	)

	DescribeTable("derives the prefix from the single clean wildcard subject",
		func(subjects []string, want string) {
			prefix, err := derivePrefix(subjects)
			Expect(err).ToNot(HaveOccurred())
			Expect(prefix).To(Equal(want))
		},
		Entry("a single-token prefix", []string{"runs.>"}, "runs"),
		Entry("a multi-token prefix", []string{"fisk.sessions.>"}, "fisk.sessions"),
		Entry("ignoring literal operator subjects alongside it", []string{"ops.audit", "runs.>", "ops.metrics"}, "runs"),
	)
})

var _ = Describe("subject helpers", func() {
	s := &store{prefix: "runs"}

	It("Should place the meta record at the _meta subject", func() {
		Expect(s.metaSubject("abc")).To(Equal("runs.abc._meta"))
		Expect(s.subjectForSeq("abc", 1)).To(Equal("runs.abc._meta"))
	})

	It("Should place later records at their numeric seq subject", func() {
		Expect(s.subjectForSeq("abc", 2)).To(Equal("runs.abc.2"))
		Expect(s.subjectForSeq("abc", 42)).To(Equal("runs.abc.42"))
	})

	It("Should match every record of a run with the run wildcard", func() {
		Expect(s.runWildcard("abc")).To(Equal("runs.abc.>"))
	})

	DescribeTable("maps a record subject back to its seq",
		func(subject string, want uint64) {
			seq, err := parseSeqToken(subject)
			Expect(err).ToNot(HaveOccurred())
			Expect(seq).To(Equal(want))
		},
		Entry("the meta token is seq 1", "runs.abc._meta", uint64(1)),
		Entry("a numeric token is its value", "runs.abc.7", uint64(7)),
		Entry("a multi-token prefix still resolves the tail", "fisk.sessions.abc.10", uint64(10)),
	)

	It("Should error on an unparsable seq token", func() {
		_, err := parseSeqToken("runs.abc.notanumber")
		Expect(err).To(HaveOccurred())
	})

	DescribeTable("extracts a run id from a meta subject",
		func(subject, wantID string, wantOK bool) {
			id, ok := s.runIDFromMetaSubject(subject)
			Expect(ok).To(Equal(wantOK))
			Expect(id).To(Equal(wantID))
		},
		Entry("a well-formed meta subject", "runs.abc._meta", "abc", true),
		Entry("a record subject is not a meta subject", "runs.abc.2", "", false),
		Entry("a subject outside the prefix is ignored", "other.abc._meta", "", false),
		Entry("an id with a dot is not a valid run id", "runs.a.b._meta", "", false),
	)
})
