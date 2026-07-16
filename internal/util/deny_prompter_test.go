//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

var _ = Describe("DefaultDenyPrompter", func() {
	ctx := context.Background()
	p := DefaultDenyPrompter()

	It("fails closed on every method, returning both a denial value and an error", func() {
		choice, err := p.ApproveCommand(ctx, GateRequest{})
		Expect(err).To(HaveOccurred())
		Expect(choice).To(Equal(ConfirmNo))

		ok, err := p.Confirm(ctx, "proceed?")
		Expect(err).To(HaveOccurred())
		Expect(ok).To(BeFalse())

		idx, err := p.Select(ctx, "which?", []string{"a", "b"})
		Expect(err).To(HaveOccurred())
		Expect(idx).To(Equal(-1))

		val, err := p.Input(ctx, "value?", "default")
		Expect(err).To(HaveOccurred())
		Expect(val).To(BeEmpty())
	})
})

var _ = Describe("BuiltinTool MCP accessors", func() {
	ctx := context.Background()
	cfg := &config.Config{Harness: config.HarnessConfig{RAG: &config.RAGConfig{Enabled: true}}}

	It("exposes the input schema and dispatches the handler in-process", func() {
		tools := RAGTools(cfg, nil)
		Expect(tools).To(HaveLen(1))
		ks := tools[0]
		Expect(ks.Name()).To(Equal("knowledge_search"))

		props, ok := ks.InputSchema()["properties"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(props).To(HaveKey("query"))

		// A nil store makes the handler return an invocation error without touching
		// the network; the deny prompter is inert here since the handler ignores it.
		_, err := ks.Call(ctx, json.RawMessage(`{"query":"x"}`), DefaultDenyPrompter())
		Expect(err).To(HaveOccurred())
	})
})
