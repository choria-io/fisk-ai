// Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Knowledge (RAG) config", func() {
	It("gates on the block and the enabled flag", func() {
		off := &Config{}
		Expect(off.RAGEnabled()).To(BeFalse())
		Expect(off.RAGVectorEnabled()).To(BeFalse())

		present := &Config{Harness: HarnessConfig{RAG: &RAGConfig{Enabled: false}}}
		Expect(present.RAGEnabled()).To(BeFalse())

		on := &Config{Harness: HarnessConfig{RAG: &RAGConfig{Enabled: true}}}
		Expect(on.RAGEnabled()).To(BeTrue())
		Expect(on.RAGVectorEnabled()).To(BeFalse(), "no embeddings block means lexical-only")
	})

	It("turns on the vector tier only when the embeddings block is present", func() {
		cfg := &Config{Harness: HarnessConfig{RAG: &RAGConfig{Enabled: true, Embeddings: &RAGEmbeddingsConfig{BaseURL: "http://127.0.0.1:1234/v1", Model: "m"}}}}
		Expect(cfg.RAGVectorEnabled()).To(BeTrue())
	})

	It("parses the embeddings timeout and defaults it when unset", func() {
		data := []byte(`
application_path: /bin/ls
identity: kb
system_prompt: hello
llm:
  model: claude-opus-4-8
harness:
  knowledge:
    enabled: true
    embeddings:
      base_url: http://127.0.0.1:1234/v1
      model: text-embedding-test
`)
		cfg, err := ParseConfig(data)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfg.RAGVectorEnabled()).To(BeTrue())
		Expect(cfg.Harness.RAG.Embeddings.TimeoutParsed).To(Equal(30 * time.Second))
	})

	It("rejects a malformed embeddings timeout", func() {
		data := []byte(`
application_path: /bin/ls
identity: kb
system_prompt: hello
llm:
  model: claude-opus-4-8
harness:
  knowledge:
    enabled: true
    embeddings:
      base_url: http://127.0.0.1:1234/v1
      model: m
      timeout: not-a-duration
`)
		_, err := ParseConfig(data)
		Expect(err).To(HaveOccurred())
	})
})
