//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	sdk "github.com/anthropics/anthropic-sdk-go"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
)

var _ = Describe("Provider.buildParams", func() {
	var (
		p      *Provider
		zeroCC sdk.CacheControlEphemeralParam
	)

	BeforeEach(func() {
		p = &Provider{}
	})

	baseReq := func() llm.Request {
		return llm.Request{
			Model:        "test-model",
			SystemBlocks: []string{"sys one", "sys two"},
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: "go"}}}}},
		}
	}

	Describe("prompt caching", func() {
		It("places both breakpoints when caching is enabled", func() {
			req := baseReq()
			req.PromptCache = true

			params, err := p.buildParams(req)
			Expect(err).NotTo(HaveOccurred())

			Expect(params.System[len(params.System)-1].CacheControl).NotTo(Equal(zeroCC), "tools+system breakpoint set")
			Expect(params.CacheControl).NotTo(Equal(zeroCC), "conversation-tail breakpoint set")
		})

		It("leaves the request's neutral system untouched, so no marker can reach the fingerprint", func() {
			req := baseReq()
			req.PromptCache = true

			_, err := p.buildParams(req)
			Expect(err).NotTo(HaveOccurred())

			// The neutral system is a plain []string built by the caller and hashed into
			// the fingerprint; buildParams marks a freshly built SDK slice, never this one.
			Expect(req.SystemBlocks).To(Equal([]string{"sys one", "sys two"}))
		})

		It("sets no breakpoints when caching is disabled", func() {
			params, err := p.buildParams(baseReq())
			Expect(err).NotTo(HaveOccurred())

			Expect(params.System[len(params.System)-1].CacheControl).To(Equal(zeroCC))
			Expect(params.CacheControl).To(Equal(zeroCC))
		})

		It("selects a 1h TTL for an interactive run and the 5m default for an autonomous one", func() {
			interactive := baseReq()
			interactive.PromptCache = true
			interactive.Interactive = true

			params, err := p.buildParams(interactive)
			Expect(err).NotTo(HaveOccurred())
			Expect(params.CacheControl.TTL).To(Equal(sdk.CacheControlEphemeralTTLTTL1h))
			Expect(params.System[len(params.System)-1].CacheControl.TTL).To(Equal(sdk.CacheControlEphemeralTTLTTL1h))

			autonomous := baseReq()
			autonomous.PromptCache = true

			params, err = p.buildParams(autonomous)
			Expect(err).NotTo(HaveOccurred())
			// The 5m default leaves TTL at its zero value (the SDK omits it).
			Expect(params.CacheControl.TTL).To(BeEmpty())
			Expect(params.System[len(params.System)-1].CacheControl.TTL).To(BeEmpty())
		})
	})

	Describe("tools", func() {
		It("renders each tool definition and appends the tool search tool only when asked", func() {
			req := baseReq()
			req.Tools = []llm.ToolDef{{Name: "shell", Description: "run a shell command"}}
			req.ToolSearch = true

			params, err := p.buildParams(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(params.Tools).To(HaveLen(2))
			Expect(params.Tools[0].OfTool).NotTo(BeNil())
			Expect(params.Tools[0].OfTool.Name).To(Equal("shell"))
			Expect(params.Tools[1].OfToolSearchToolBm25_20251119).NotTo(BeNil())
		})

		It("omits the tool search tool when no tool defers", func() {
			req := baseReq()
			req.Tools = []llm.ToolDef{{Name: "shell"}}

			params, err := p.buildParams(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(params.Tools).To(HaveLen(1))
			Expect(params.Tools[0].OfToolSearchToolBm25_20251119).To(BeNil())
		})
	})

	Describe("thinking", func() {
		It("requests summarized adaptive thinking when enabled", func() {
			req := baseReq()
			req.ThinkingEnabled = true

			params, err := p.buildParams(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(params.Thinking.OfAdaptive).NotTo(BeNil())
			Expect(params.Thinking.OfAdaptive.Display).To(Equal(sdk.ThinkingConfigAdaptiveDisplaySummarized))
		})

		It("omits thinking when disabled", func() {
			params, err := p.buildParams(baseReq())
			Expect(err).NotTo(HaveOccurred())
			Expect(params.Thinking.OfAdaptive).To(BeNil())
		})
	})

	Describe("model and messages", func() {
		It("carries the model, output cap and converted conversation", func() {
			req := baseReq()
			req.MaxOutputTokens = 4096

			params, err := p.buildParams(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(params.Model).To(Equal(sdk.Model("test-model")))
			Expect(params.MaxTokens).To(Equal(int64(4096)))
			Expect(params.Messages).To(HaveLen(1))
			Expect(params.Messages[0].Role).To(Equal(sdk.MessageParamRoleUser))
		})
	})
})

var _ = Describe("credential env vars", func() {
	// This is the authoritative assertion of the names this provider registers for
	// the tool-subprocess credential strip; internal/toolkit/fisk tests only the
	// mechanism. Only this provider is linked into its own test binary, so the
	// registry union is exactly this list.
	It("registers the secret-bearing anthropic variables", func() {
		Expect(llm.CredentialEnvNames()).To(ConsistOf(
			"ANTHROPIC_API_KEY",
			"ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_IDENTITY_TOKEN",
			"ANTHROPIC_WEBHOOK_SIGNING_KEY",
			"ANTHROPIC_CUSTOM_HEADERS",
		))
	})
})
